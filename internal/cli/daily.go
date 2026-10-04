package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
)

// ExitError carries the exit code of a command run in a container, so egzo exits with it.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("command exited with code %d", e.Code) }

// IsExitError reports the exit code when err is an ExitError.
func IsExitError(err error) (int, bool) {
	var exit ExitError
	if errors.As(err, &exit) {
		return exit.Code, true
	}
	return 0, false
}

// service finds the project's container for a service name (control, proxy, or an agent).
func (s *session) service(name string) (*stack.Resource, error) {
	var found *stack.Resource
	for i := range s.observed.Resources {
		r := &s.observed.Resources[i]
		if r.Type == "container" && r.Service == name {
			found = r
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no service %q in project %q (run `egzo up`?)", name, s.Resolved.Name)
	}
	return found, nil
}

func newLogsCommand(opts *options) *cobra.Command {
	var follow bool
	var tail string
	cmd := &cobra.Command{
		Use:   "logs SERVICE",
		Short: "Show a service's logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			return streamLogs(ctx, s, args[0], follow, tail, cmd)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log output")
	cmd.Flags().StringVar(&tail, "tail", "all", "number of lines to show from the end of the logs")
	return cmd
}

func streamLogs(ctx context.Context, s *session, name string, follow bool, tail string, cmd *cobra.Command) error {
	target, err := s.service(name)
	if err != nil {
		return err
	}
	reader, err := s.engine.API.ContainerLogs(ctx, target.ID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: follow, Tail: tail,
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = stdcopy.StdCopy(cmd.OutOrStdout(), cmd.ErrOrStderr(), reader)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func newExecCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "exec SERVICE -- COMMAND [ARG...]",
		Short: "Run a command in a service's container",
		Long: "Run a command in a service's container. With a terminal on both ends it gets a terminal of its own,\n" +
			"and window resizes follow. egzo exits with the command's exit code.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			target, err := s.service(args[0])
			if err != nil {
				return err
			}
			return runIn(ctx, s.engine, target.ID, args[1:])
		},
	}
}

// runIn runs a command in a container, with a terminal when stdin and stdout both are terminals.
func runIn(ctx context.Context, c *engine.Client, id string, command []string) error {
	tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	stream := engine.Stream{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, TTY: tty}

	var resized chan struct{}
	if tty {
		stream.Size = func() (uint, uint) {
			width, height, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				return 80, 24
			}
			return uint(width), uint(height)
		}
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(os.Stdin.Fd()), state)

		resized = make(chan struct{}, 1)
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				select {
				case resized <- struct{}{}:
				default:
				}
			}
		}()
	}

	code, err := c.ExecStream(ctx, id, command, stream, resized)
	if err != nil {
		return err
	}
	if code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

func newLifecycleCommand(opts *options, verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " SERVICE",
		Short: short,
		Long:  short + ". This works on the existing container and never reconciles: use `egzo up` to apply changes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			target, err := s.service(args[0])
			if err != nil {
				return err
			}
			timeout := 5
			switch verb {
			case "start":
				err = s.engine.API.ContainerStart(ctx, target.ID, container.StartOptions{})
			case "stop":
				err = s.engine.API.ContainerStop(ctx, target.ID, container.StopOptions{Timeout: &timeout})
			case "restart":
				err = s.engine.API.ContainerRestart(ctx, target.ID, container.StopOptions{Timeout: &timeout})
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, args[0])
			return nil
		},
	}
}

func newProxyLogCommand(opts *options) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show the proxy's audit trail, one JSON event per line",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			return streamLogs(ctx, s, "proxy", follow, "all", cmd)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the audit trail")
	return cmd
}
