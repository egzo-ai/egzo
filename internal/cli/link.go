// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/link"
	"github.com/egzo-ai/egzo/internal/operator"
	"github.com/egzo-ai/egzo/internal/stack"
)

const (
	linkOperatorSocket = "/run/egzo/link.sock"
	linkStateDir       = "/var/lib/egzo/link"
)

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
	return mux
}
