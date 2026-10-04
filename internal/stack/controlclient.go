package stack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/egzo-ai/egzo/internal/engine"
)

// ControlRequest sends one request to the control sidecar's operator API through exec and returns the
// response body. It is how the CLI, and later the hub, talk to the control sidecar.
func ControlRequest(ctx context.Context, c *engine.Client, project, method, path string, body []byte) ([]byte, error) {
	var stdin io.Reader
	if body != nil {
		stdin = bytes.NewReader(body)
	}
	result, err := c.Exec(ctx, project+"-control-1", []string{"/egzo", "control", "request", method, path}, stdin)
	if err != nil {
		return nil, fmt.Errorf("control sidecar: %w (is the project up?)", err)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(string(result.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(result.Stdout))
		}
		return nil, fmt.Errorf("control sidecar: %s", message)
	}
	return result.Stdout, nil
}

// ControlStream runs a streaming request in the control sidecar and copies its output to out.
func ControlStream(ctx context.Context, c *engine.Client, project, method, path string, out, errOut io.Writer) error {
	code, err := c.ExecStream(ctx, project+"-control-1", []string{"/egzo", "control", "stream", method, path},
		engine.Stream{Out: out, Err: errOut}, nil)
	if err != nil {
		return fmt.Errorf("control sidecar: %w (is the project up?)", err)
	}
	if code != 0 {
		return fmt.Errorf("control sidecar: stream ended with code %d", code)
	}
	return nil
}
