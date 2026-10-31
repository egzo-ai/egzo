// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/portforward"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// newPortForwardCommand is `egzo agent port-forward`: a listener where the CLI runs, and one exec of `egzo agent forward` in the
// agent's container for each connection it accepts.
func newPortForwardCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "port-forward INSTANCE [[ADDRESS:]LOCAL:]PORT",
		Short: "Reach a port of an agent from this machine",
		Long: "Listen on 127.0.0.1:LOCAL of the machine this command runs on (LOCAL defaults to PORT, 0 lets the system choose, ADDRESS\n" +
			"listens elsewhere) and carry each connection to PORT on the agent's own loopback, through the engine. Nothing is published\n" +
			"and the agent's network does not change. With DOCKER_HOST=ssh://box on a laptop, the port is on the laptop. Ctrl-C ends it;\n" +
			"it ends by itself when the agent stops. The agent's image must contain egzo, as for `egzo agent attach`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapping, err := portforward.ParseMapping(args[1])
			if err != nil {
				return err
			}
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			name := args[0]
			target, err := s.service(name)
			if err != nil {
				return err
			}
			if target.Kind != "agent" {
				return fmt.Errorf("%s is not an agent: it has no ports to forward", name)
			}
			if target.State != "running" {
				return fmt.Errorf("agent %q is %s: start it with `egzo agent start %s`", name, target.State, name)
			}
			// Before listening: what a failed exec says would reach the client as if the server had said it.
			if err := canForward(ctx, s.engine, target.ID, name); err != nil {
				return err
			}
			listener, err := net.Listen("tcp", mapping.Listen())
			if err != nil {
				return err
			}
			defer listener.Close()
			fmt.Fprintf(cmd.OutOrStdout(), "forwarding %s to port %d of %s (Ctrl-C ends it)\n", listener.Addr(), mapping.Port, name)

			f := forwarder{engine: s.engine, id: target.ID, name: name, port: mapping.Port, report: func(line string) {
				fmt.Fprintln(cmd.ErrOrStderr(), line)
			}}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ended := make(chan error, 1)
			go func() { ended <- f.waitStop(ctx); cancel() }()
			go func() {
				<-ctx.Done()
				listener.Close()
			}()
			for {
				conn, err := listener.Accept()
				if err != nil {
					select {
					case err := <-ended:
						return err
					case <-ctx.Done():
						return <-ended
					default:
						return err
					}
				}
				go f.carry(ctx, conn)
			}
		},
	}
}

// canForward asks the egzo in the agent's image for a port that it refuses: only one that knows `agent forward` says so.
func canForward(ctx context.Context, c *engine.Client, id, name string) error {
	result, err := c.Exec(ctx, id, portforward.Command(0), nil)
	if err != nil && ctx.Err() != nil {
		return err
	}
	if !strings.Contains(string(result.Stderr), portforward.NotAPort) {
		return fmt.Errorf("agent %q cannot forward a port: its image does not contain egzo, or one older than `egzo agent forward` "+
			"(the egzo harness images of this version do)", name)
	}
	return nil
}

type forwarder struct {
	engine *engine.Client
	id     string
	name   string
	port   int
	report func(line string)
}

// waitStop returns when the agent stops, with what to say; nil when the context ended first (Ctrl-C).
func (f forwarder) waitStop(ctx context.Context) error {
	stopped, failed := f.engine.API.ContainerWait(ctx, f.id, container.WaitConditionNotRunning)
	select {
	case <-stopped:
		return fmt.Errorf("agent %q stopped", f.name)
	case err := <-failed:
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("following agent %q: %w", f.name, err)
	}
}

// carry is one connection: one exec. A failure ends this connection only: the server may start later.
func (f forwarder) carry(ctx context.Context, conn net.Conn) {
	defer hangUp(conn)
	var said lastWords
	code, err := f.engine.ExecStream(ctx, f.id, portforward.Command(f.port), engine.Stream{In: conn, Out: conn, Err: &said}, nil)
	switch {
	case ctx.Err() != nil:
	case err != nil:
		f.report(fmt.Sprintf("%s:%d: %v", f.name, f.port, err))
	case code != 0:
		f.report(fmt.Sprintf("%s:%d: %s", f.name, f.port, termsafe.Truncate(termsafe.Line(strings.TrimSpace(string(said))), 200)))
	}
}

// hangUpTimeout is how long a connection whose exec ended goes on taking what the client still sends.
var hangUpTimeout = 5 * time.Second

// hangUp closes a connection as a server does, the answer first. The exec ends with the server's FIN, and a client still sending
// (a server refused its upload early, and only half closed) would be reset by a close with its bytes unread, which can destroy an
// answer it has not read yet. What it sends now goes nowhere: an exec's stdout cannot end before the exec does.
func hangUp(conn net.Conn) {
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(hangUpTimeout))
	_, _ = io.Copy(io.Discard, conn)
}

// lastWords keeps the start of what a command says on stderr: one line, written by a program in the agent's container.
type lastWords []byte

func (w *lastWords) Write(data []byte) (int, error) {
	if room := 1024 - len(*w); room > 0 {
		*w = append(*w, data[:min(room, len(data))]...)
	}
	return len(data), nil
}
