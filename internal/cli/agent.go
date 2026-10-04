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
	"github.com/egzo-ai/egzo/internal/harness"
	sess "github.com/egzo-ai/egzo/internal/session"
)

// newAgentCommand holds the roles that run inside an agent container.
func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Roles that run inside an agent container",
		Hidden: true,
	}
	run := &cobra.Command{
		Use:   "run [--] [PROGRAM [ARG...]]",
		Short: "Run a harness on a terminal that clients can attach to (the session holder)",
		Long: "Run a harness TUI on a terminal inside the agent container: prepare its configuration for its\n" +
			"harness (EGZO_HARNESS), run it, serve `egzo attach`, report its state to the control sidecar and\n" +
			"type queued messages into it. This is the entrypoint of an egzo harness image.",
		RunE: func(cmd *cobra.Command, args []string) error { return runAgent(args) },
	}
	run.Flags().SetInterspersed(false)
	attach := &cobra.Command{
		Use:   "attach",
		Short: "Attach this terminal to the session of this container",
		Args:  cobra.NoArgs,
	}
	var readOnly bool
	var detachKeys string
	attach.Flags().BoolVar(&readOnly, "read-only", false, "watch the session without typing into it")
	attach.Flags().StringVar(&detachKeys, "detach-keys", "ctrl-]", "the key that detaches")
	attach.RunE = func(cmd *cobra.Command, args []string) error {
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
	cmd.AddCommand(run, attach)
	return cmd
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

	if hasIntegration {
		home := os.Getenv("HOME")
		if home == "" {
			home = "/home/agent"
		}
		options := harness.Options{
			Agent:            os.Getenv("EGZO_AGENT"),
			Home:             home,
			Model:            os.Getenv("EGZO_MODEL"),
			Bypass:           os.Getenv("EGZO_PERMISSIONS") != "default",
			ControlURL:       os.Getenv("EGZO_CONTROL_URL"),
			InstructionsFile: home + "/.egzo/instructions.md",
		}
		if workspaces := os.Getenv("EGZO_WORKSPACES"); workspaces != "" {
			options.Workspaces = strings.Split(workspaces, ":")
		}
		options.Workdir, _ = os.Getwd()
		if agent, token := os.Getenv("EGZO_AGENT"), os.Getenv("EGZO_TOKEN"); agent != "" && token != "" {
			options.Authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(agent+":"+token))
		}
		if path := os.Getenv("EGZO_PROMPT_FILE"); path != "" {
			prompt, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read the agent's prompt: %w", err)
			}
			options.Prompt = string(prompt)
		}
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
	if hasIntegration {
		idle = integration.IdleSignal()
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
			api.Do(ctx, "POST", "/v1/hooks/"+args[0], payload)
			return nil
		},
	}
}
