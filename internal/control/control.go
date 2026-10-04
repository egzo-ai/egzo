// Package control is the control sidecar: it will hold agent status, the event log and the
// message queue. For now it serves the operator API on a unix socket inside the container,
// reachable only through `engine exec`.
package control

import (
	"io"
	"path/filepath"

	"github.com/egzo-ai/egzo/internal/operator"
)

// StateDir is where the control sidecar keeps its volume.
const StateDir = "/state"

// SocketPath is the operator API socket.
var SocketPath = filepath.Join(StateDir, "operator.sock")

// Run serves the operator API until SIGTERM or SIGINT.
func Run() error {
	if err := ensureState(StateDir); err != nil {
		return err
	}
	return operator.Serve(SocketPath, newServer(StateDir).handler())
}

// Healthcheck succeeds when the operator API answers.
func Healthcheck() error { return operator.Healthcheck(SocketPath) }

// Request is the client the CLI runs inside the container (`egzo control request`).
func Request(method, path string, body io.Reader) ([]byte, error) {
	return operator.Request(SocketPath, method, path, body)
}
