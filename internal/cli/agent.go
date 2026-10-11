// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/agentclient"
	"github.com/egzo-ai/egzo/internal/browser"
	"github.com/egzo-ai/egzo/internal/harness"
	sess "github.com/egzo-ai/egzo/internal/session"
)

// newAgentCommand is the group of commands about agents: the ones you run (spawn, ls, rm, start, stop, attach) and,
// hidden, the roles that run inside an agent container (run, browser).
func newAgentCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Spawn, list, remove, start, stop and attach to agents",
	}
	run := &cobra.Command{
		Use:    "run [--] [PROGRAM [ARG...]]",
		Short:  "Run a harness on a terminal that clients can attach to (the session holder)",
		Hidden: true,
		Long: "Run a harness TUI on a terminal inside the agent container: prepare its configuration for its\n" +
			"harness (EGZO_HARNESS), run it, serve `egzo agent attach`, report its state to the control sidecar and\n" +
			"type queued messages into it. This is the entrypoint of an egzo harness image.",
		RunE: func(cmd *cobra.Command, args []string) error { return runAgent(args) },
	}
	run.Flags().SetInterspersed(false)
	browserCommand := &cobra.Command{
		Use:    "browser",
		Short:  "Run the Playwright MCP server, configured for this agent's proxy",
		Hidden: true,
		Long: "Run `playwright-mcp` with a configuration built from this container's environment: the proxy of\n" +
			"HTTPS_PROXY with its credentials as separate fields, a headless isolated Chromium, and its output in a\n" +
			"temporary directory. The harness's MCP configuration starts this instead of `playwright-mcp`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := browser.Run(os.Environ(), os.Stdin, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			if code != 0 {
				return ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.AddCommand(newSpawnCommand(opts), newAgentListCommand(opts), newAgentRmCommand(opts),
		newLifecycleCommand(opts, "start", "Start an agent's container"), newLifecycleCommand(opts, "stop", "Stop an agent's container"),
		newAttachCommand(opts), run, browserCommand)
	return group(cmd)
}

// attachInContainer attaches this terminal to the session of the container egzo runs in: what the CLI runs
// in an agent's container (`egzo agent attach`, with no instance) to attach you to it.
func attachInContainer(detachKeys string, readOnly bool) error {
	key, err := sess.DetachKeys(detachKeys)
	if err != nil {
		return err
	}
	code, err := sess.Attach(sess.SocketPath(), readOnly, key, os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	if value := os.Getenv(name); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func runAgent(args []string) error {
	integration, hasIntegration := harness.For(os.Getenv("EGZO_HARNESS"))
	command := args
	environment := os.Environ()
	interrupt := "\x03"

	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/agent"
	}
	// Chromium reads its own database, not SSL_CERT_FILE. Best effort: a browser that does not trust the
	// project CA is a smaller problem than a harness that does not start.
	if err := browser.TrustProjectCA(home, browser.ProjectCA, exec.LookPath, browser.ExecRunner); err != nil {
		fmt.Fprintf(os.Stderr, "egzo: could not trust the project CA in the browser's database: %v\n", err)
	}

	if hasIntegration {
		options := harness.Options{
			Agent:            os.Getenv("EGZO_AGENT"),
			Home:             home,
			Model:            os.Getenv("EGZO_MODEL"),
			Bypass:           os.Getenv("EGZO_PERMISSIONS") != "default",
			ControlURL:       os.Getenv("EGZO_CONTROL_URL"),
			InstructionsFile: home + "/.egzo/instructions.md",
			LookPath:         exec.LookPath,
		}
		if workspaces := os.Getenv("EGZO_WORKSPACES"); workspaces != "" {
			options.Workspaces = strings.Split(workspaces, ":")
		}
		options.Workdir, _ = os.Getwd()
		if agent, token := os.Getenv("EGZO_AGENT"), os.Getenv("EGZO_TOKEN"); agent != "" && token != "" {
			options.Authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(agent+":"+token))
		}
		options.Prompt = os.Getenv("EGZO_PROMPT")
		plan, err := integration.Plan(options, args)
		if err != nil {
			return err
		}
		if err := plan.Write(); err != nil {
			return fmt.Errorf("prepare %s: %w", integration.Name(), err)
		}
		command, interrupt = plan.Command, plan.InterruptKey
		for key, value := range plan.Env {
			environment = append(environment, key+"="+value)
		}
	}
	if len(command) == 0 {
		return fmt.Errorf("no program to run: pass one after --, or set EGZO_HARNESS to a harness with a default")
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return fmt.Errorf("cannot run %s: %w", command[0], err)
	}
	environment = withDefault(environment, "TERM", "xterm-256color")
	environment = withDefault(environment, "COLORTERM", "truecolor")

	holder, err := sess.Start(command, environment, "")
	if err != nil {
		return err
	}
	listener, err := sess.Listen(sess.SocketPath())
	if err != nil {
		holder.Terminate()
		return err
	}
	defer listener.Close()
	go holder.Serve(listener)
	holder.ForwardSignals()

	idle := "quiescence"
	var readyMarkers []string
	if hasIntegration {
		idle = integration.IdleSignal()
		readyMarkers = integration.ReadyMarkers()
	}
	if value := os.Getenv("EGZO_IDLE_SIGNAL"); value == "hook" || value == "quiescence" {
		idle = value
	}
	if api, err := agentclient.FromEnv(); err == nil {
		go holder.RunDelivery(context.Background(), api, sess.Delivery{
			HumanQuiet:   durationEnv("EGZO_HUMAN_QUIET", 30*time.Second),
			AckTimeout:   durationEnv("EGZO_ACK_TIMEOUT", 60*time.Second),
			IdleSignal:   idle,
			Quiescence:   durationEnv("EGZO_QUIESCENCE", 5*time.Second),
			InterruptKey: interrupt,
			RequireRaw:   hasIntegration, // a known TUI: it is ready when it has taken the terminal over
			ReadyMarkers: readyMarkers,
		})
	}

	<-holder.Done()
	// Nobody may have been attached: leave what the program last wrote where `egzo logs` finds it.
	if tail := holder.Output(); len(tail) > 0 {
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		os.Stderr.Write(append(tail, '\n'))
	}
	if code := holder.ExitCode(); code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

func withDefault(environment []string, key, value string) []string {
	for _, entry := range environment {
		if strings.HasPrefix(entry, key+"=") {
			return environment
		}
	}
	return append(environment, key+"="+value)
}

// newHookCommand is what a harness's hooks run: it posts the hook's payload to the control sidecar.
// It never fails and never waits long, so it can never hold the harness up.
func newHookCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "hook NAME",
		Short:  "Report a harness hook to the control sidecar (reads the payload on stdin)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := []byte("{}")
			if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
				done := make(chan []byte, 1)
				go func() { data, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); done <- data }()
				select {
				case data := <-done:
					if len(strings.TrimSpace(string(data))) > 0 {
						payload = data
					}
				case <-time.After(2 * time.Second):
				}
			}
			api, err := agentclient.FromEnv()
			if err != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			postHook(ctx, api, args[0], payload)
			return nil
		},
	}
}

// postHook delivers a hook to the control sidecar, trying again for as long as the context allows when
// the sidecar is busy or not reachable yet: a lost "Stop" would keep the agent "working" for good. It
// gives up at once on a refusal, which no retry would change.
func postHook(ctx context.Context, api *agentclient.Client, name string, payload []byte) error {
	delay := 100 * time.Millisecond
	for {
		_, err := api.Do(ctx, "POST", "/v1/hooks/"+name, payload)
		if err == nil || !agentclient.Temporary(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Second)
	}
}
