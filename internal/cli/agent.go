// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/agentclient"
	"github.com/egzo-ai/egzo/internal/browser"
	"github.com/egzo-ai/egzo/internal/harness"
	"github.com/egzo-ai/egzo/internal/portforward"
	"github.com/egzo-ai/egzo/internal/proxy"
	sess "github.com/egzo-ai/egzo/internal/session"
)

// newAgentCommand is the group of commands about agents: the ones you run (spawn, ls, rm, start, stop, attach)
// and, hidden, the roles that run inside an agent container (run, browser, forward).
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
		Long: "Run `playwright-mcp` with a configuration built from this container's environment: a forwarder on\n" +
			"127.0.0.1 that adds the credentials of HTTPS_PROXY to everything Chromium sends, a headless isolated Chromium,\n" +
			"and its output in a temporary directory. The harness's MCP configuration starts this instead of `playwright-mcp`.",
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
	var announce, closeOnEOF bool
	forward := &cobra.Command{
		Use:    "forward PORT",
		Short:  "Carry one connection to a port of this container's loopback over stdin and stdout",
		Hidden: true,
		Long: "Connect to PORT on this container's loopback and copy bytes between it and stdin and stdout. What\n" +
			"the project's link runs in the agent's container for a preview, once per connection. It takes a port and never an\n" +
			"address: it reaches nothing but this container. --announce says on stderr when it reached the server, and\n" +
			"--close-on-eof ends at the end of stdin, for a client that never half closes.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := portforward.ParsePort(args[0])
			if err != nil {
				return err
			}
			options := portforward.Options{CloseOnEOF: closeOnEOF}
			if announce {
				options.Connected = func() { fmt.Fprintln(os.Stderr, portforward.Connected) }
			}
			if err := portforward.Forward(port, os.Stdin, os.Stdout, options); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return ExitError{Code: 1}
			}
			return nil
		},
	}
	forward.Flags().BoolVar(&announce, "announce", false, "say on stderr when the server has been reached")
	forward.Flags().BoolVar(&closeOnEOF, "close-on-eof", false, "end at the end of stdin")
	cmd.AddCommand(newSpawnCommand(opts), newAgentListCommand(opts), newAgentRmCommand(opts),
		newLifecycleCommand(opts, "start", "Start an agent's container"), newLifecycleCommand(opts, "stop", "Stop an agent's container"),
		newAttachCommand(opts), run, browserCommand, forward, newSSHSetupCommand())
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

	// Best effort, like the browser: an agent that cannot ssh is a smaller problem than a harness that does not start.
	if err := setupSSH(home, os.Getenv("EGZO_TOKEN"), filepath.Join(proxy.AgentCADir, "ssh_config")); err != nil {
		fmt.Fprintf(os.Stderr, "egzo: could not set up ssh: %v\n", err)
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
			Getenv:           os.Getenv,
		}
		if workspaces := os.Getenv("EGZO_WORKSPACES"); workspaces != "" {
			options.Workspaces = strings.Split(workspaces, ":")
		}
		options.Workdir, _ = os.Getwd()
		if agent, token := os.Getenv("EGZO_AGENT"), os.Getenv("EGZO_TOKEN"); agent != "" && token != "" {
			options.Authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(agent+":"+token))
		}
		options.Prompt = os.Getenv("EGZO_PROMPT")
		ports, err := harness.ParsePorts(os.Getenv(harness.PortsEnv))
		if err != nil {
			return err
		}
		options.Ports = ports
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
		runSetup(plan.Setup, environment)
	}
	if len(command) == 0 {
		return fmt.Errorf("no program to run: pass one after --, or set EGZO_HARNESS to a harness with a default")
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return fmt.Errorf("cannot run %s: %w", command[0], err)
	}
	environment = withDefault(environment, "TERM", "xterm-256color")
	environment = withDefault(environment, "COLORTERM", "truecolor")

	reportBoot()
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
	if value := os.Getenv("EGZO_IDLE_SIGNAL"); value == "state" || value == "quiescence" {
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

// bootLimit is how long a harness waits for the control sidecar to hear that the sandbox is new. An agent starts once the sidecar is healthy,
// so this is for a sidecar that is down, and a harness that does not start is the bigger problem.
const bootLimit = 10 * time.Second

// reportBoot tells the control sidecar that this sandbox is new, which ends what an earlier boot exposed. It comes before the harness starts:
// the first thing a harness does may be to expose a port, and that must not be undone.
func reportBoot() {
	api, err := agentclient.FromEnv()
	if err != nil {
		return // no control sidecar to tell, as for delivery
	}
	ctx, cancel := context.WithTimeout(context.Background(), bootLimit)
	defer cancel()
	for {
		_, err := api.Do(ctx, "POST", "/v1/boot", nil)
		if err == nil {
			return
		}
		if !agentclient.Temporary(err) || ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "egzo: what an earlier boot exposed may still be exposed: %v\n", err)
			return
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

func withDefault(environment []string, key, value string) []string {
	for _, entry := range environment {
		if strings.HasPrefix(entry, key+"=") {
			return environment
		}
	}
	return append(environment, key+"="+value)
}

// newStateCommand is what a harness runs to report its state to the control sidecar. It never waits long
// on the sidecar, so it can never hold the harness up. A harness that passes its own event on stdin (a
// JSON object) has its tool_name and prompt forwarded; the control sidecar records no more than that.
func newStateCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:    "state STATE",
		Short:  "Report the agent's state (starting, idle, working or blocked) to the control sidecar",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state := args[0]
			if !validStates[state] {
				return fmt.Errorf("state %q is not starting, idle, working or blocked", state)
			}
			report := map[string]any{"state": state}
			if reason != "" {
				report["reason"] = reason
			}
			if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
				done := make(chan []byte, 1)
				go func() { data, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); done <- data }()
				select {
				case data := <-done:
					var event map[string]json.RawMessage
					if json.Unmarshal(data, &event) == nil {
						for _, field := range []string{"tool_name", "prompt"} {
							if value, ok := event[field]; ok {
								report[field] = value
							}
						}
					}
				case <-time.After(2 * time.Second):
				}
			}
			payload, _ := json.Marshal(report)
			api, err := agentclient.FromEnv()
			if err != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			postState(ctx, api, payload)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why, in a few words, for people (shown by `egzo ps`)")
	return cmd
}

var validStates = map[string]bool{"starting": true, "idle": true, "working": true, "blocked": true}

// postState delivers a state to the control sidecar, trying again for as long as the context allows when
// the sidecar is busy or not reachable yet: a lost "idle" would keep the agent "working" for good. It
// gives up at once on a refusal, which no retry would change.
func postState(ctx context.Context, api *agentclient.Client, payload []byte) error {
	delay := 100 * time.Millisecond
	for {
		_, err := api.Do(ctx, "POST", "/v1/state", payload)
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

// setupTimeout bounds each setup command of a harness: one that hangs must not keep the harness from starting.
const setupTimeout = 2 * time.Minute

// runSetup runs a harness's setup commands in order, with its environment, before it starts. Best effort, like the
// browser: a failure is reported in the container's log, and the harness starts anyway (Claude Code then asks to sign in).
func runSetup(commands [][]string, environment []string) {
	for _, command := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Env, cmd.Stdout, cmd.Stderr = environment, os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "egzo: %s: %v\n", strings.Join(command, " "), err)
		}
		cancel()
	}
}
