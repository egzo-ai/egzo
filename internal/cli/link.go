// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/link"
	"github.com/egzo-ai/egzo/internal/operator"
	"github.com/egzo-ai/egzo/internal/stack"
)

const (
	linkOperatorSocket = "/run/egzo/link.sock"
	linkStateDir       = "/var/lib/egzo/link"
)

// maxLinkCode is more than any code is long: what stdin holds beyond it is not a code.
const maxLinkCode = 256

// newRegisterCommand is `egzo project register`: the link presents a link code to the hub, once. The code is asked for, or read from stdin,
// and goes to the link through `engine exec` stdin: there is no flag for it, so it is never in a shell's history or in `ps`.
func newRegisterCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "register",
		Short: "Register the project on its hub with a link code",
		Long: "An operator of the hub makes a link code (Settings, Links). This command asks for it, or reads it from stdin when\n" +
			"that is not a terminal, and has the project's link present it, once. From then on the link's key is all it takes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			if s.Resolved.Hub == nil {
				return errors.New("this project names no hub: add `hub:` to the project file, then run `egzo project up`")
			}
			status, err := stack.LinkStatus(ctx, s.engine, s.Resolved.Name, 0)
			if err != nil {
				return errors.New("the link is not running: `egzo project up` starts it")
			}
			switch status.State {
			case hublink.StateApproved:
				return errors.New("this project is registered already: " + status.Text())
			case hublink.StateLocked, hublink.StateNoEngine:
				return errors.New(status.Text())
			}
			code, err := readLinkCode(cmd)
			if err != nil {
				return err
			}
			status, err = stack.RegisterLink(ctx, s.engine, s.Resolved.Name, code)
			if err != nil {
				return err
			}
			switch status.State {
			case hublink.StateApproved:
				fmt.Fprintln(cmd.OutOrStdout(), "link: "+status.Text())
				return nil
			case hublink.StateUnregistered:
				return errors.New("the hub did not take this code: it is wrong, expired or used. Ask an operator of the hub for a new one")
			case hublink.StateRevoked:
				return errors.New(status.Text() + ": an operator of the hub has to forget this project before it can register again")
			}
			return errors.New(status.Text())
		},
	}
}

// readLinkCode asks for the code on a terminal, without showing it, and reads it from stdin otherwise.
func readLinkCode(cmd *cobra.Command) (string, error) {
	var code []byte
	var err error
	if in, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "Link code: ")
		code, err = term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
	} else {
		code, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxLinkCode+1))
	}
	if err != nil {
		return "", fmt.Errorf("read the code: %w", err)
	}
	text := strings.TrimSpace(string(code))
	if text == "" || len(text) > maxLinkCode {
		return "", errors.New("that is not a link code: an operator of the hub makes one in Settings, Links")
	}
	return text, nil
}

// newLinkCommand is the link sidecar role. It runs inside the egzo image, not on the user's machine.
func newLinkCommand() *cobra.Command {
	var healthcheck bool
	cmd := &cobra.Command{
		Use:    "link",
		Short:  "Run the project's link to its hub",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if healthcheck {
				return operator.Healthcheck(linkOperatorSocket)
			}
			return runLink()
		},
	}
	cmd.Flags().BoolVar(&healthcheck, "healthcheck", false, "exit 0 when the sidecar is healthy")
	cmd.AddCommand(&cobra.Command{
		Use:   "request METHOD PATH",
		Short: "Send one request to the operator API, with stdin as the body",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := operator.Request(linkOperatorSocket, args[0], args[1], cmd.InOrStdin())
			os.Stdout.Write(body)
			return err
		},
	}, &cobra.Command{
		Use:   "socket-group PATH",
		Short: "Print the group that owns a socket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var info syscall.Stat_t
			if err := syscall.Stat(args[0], &info); err != nil {
				return err
			}
			if info.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
				return fmt.Errorf("%s is not a socket", args[0])
			}
			fmt.Println(info.Gid)
			return nil
		},
	})
	return cmd
}

func runLink() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := link.Options{Project: os.Getenv("EGZO_PROJECT"), Hub: os.Getenv("EGZO_HUB"), CA: []byte(os.Getenv("EGZO_HUB_CA")), Dir: linkStateDir}
	client, err := engine.Dial(ctx, engine.Target{Host: "unix://" + stack.LinkEngineSocket})
	if err == nil {
		options.Engine = link.NewEngine(client)
		defer client.Close()
	}
	l := link.New(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "the engine's socket: %v\n", err)
		l.NoEngine(err)
	}
	go func() {
		if err := l.Run(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "the link: %v\n", err)
		}
	}()
	return operator.Serve(linkOperatorSocket, linkOperator(l))
}

// linkOperator is the link's operator API: reached by `engine exec` only, by whoever holds the engine.
func linkOperator(l *link.Link) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		settle, _ := time.ParseDuration(r.URL.Query().Get("settle"))
		_ = json.NewEncoder(w).Encode(l.Settled(r.Context(), settle))
	})
	mux.HandleFunc("PUT /unlock", func(w http.ResponseWriter, r *http.Request) {
		var body stack.LinkUnlock
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "the body is not what `up` sends", http.StatusBadRequest)
			return
		}
		if err := l.Unlock([]byte(body.SecretKey), body.Proxy); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
		}
	})
	mux.HandleFunc("PUT /rekey", func(w http.ResponseWriter, r *http.Request) {
		var body stack.LinkUnlock
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "the body is not what `rotate-ca` sends", http.StatusBadRequest)
			return
		}
		if err := l.Rekey([]byte(body.SecretKey)); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
		}
	})
	mux.HandleFunc("PUT /register", func(w http.ResponseWriter, r *http.Request) {
		var body stack.LinkRegistration
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
			http.Error(w, "the body is not what `project register` sends", http.StatusBadRequest)
			return
		}
		status, err := l.Register(r.Context(), body.Code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("POST /unlink", func(w http.ResponseWriter, r *http.Request) {
		state, err := l.Unlink(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, state)
	})
	return mux
}
