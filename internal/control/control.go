// Package control is the control sidecar: it holds the agents' tokens, the typed event stream, the
// message queue, questions and spec snapshots. It has two listeners: the operator API on a unix
// socket inside the container, reached only through `engine exec`, and the agent API on the
// project's agent networks, authenticated by each agent's own token.
package control

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/egzo-ai/egzo/internal/operator"
)

// StateDir is where the control sidecar keeps its volume.
const StateDir = "/state"

// SocketPath is the operator API socket.
var SocketPath = filepath.Join(StateDir, "operator.sock")

// Run serves both APIs until SIGTERM or SIGINT.
func Run() error {
	if err := ensureState(StateDir); err != nil {
		return err
	}
	srv, err := newServer(StateDir)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", ":"+AgentPort)
	if err != nil {
		return err
	}
	agents := agentServer((&agentAPI{server: srv}).handler())
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		agents.Shutdown(shutdown)
	}()
	go func() {
		if err := agents.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			os.Stderr.WriteString("agent API: " + err.Error() + "\n")
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-time.After(500 * time.Millisecond):
				srv.expire(now)
			}
		}
	}()
	return operator.Serve(SocketPath, srv.handler())
}

// Healthcheck succeeds when the operator API answers.
func Healthcheck() error { return operator.Healthcheck(SocketPath) }

// Request is the client the CLI runs inside the container (`egzo control request`).
func Request(method, path string, body io.Reader) ([]byte, error) {
	return operator.Request(SocketPath, method, path, body)
}

// Stream copies a long-lived response, such as the event stream, to out as it arrives.
func Stream(method, path string, out io.Writer) error {
	return operator.Stream(SocketPath, method, path, out)
}

// agentServer is the agent port's HTTP server. Agents are the less trusted side, so a client that is
// slow to send, slow to read or just idle cannot hold a connection (and its goroutine) forever.
func agentServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}
