package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ExecResult is the outcome of a command run inside a container.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Exec runs command in a container and feeds it stdin (which may be nil). It is how the CLI and the
// hub talk to the sidecars: nothing is published on the network, and what is piped through stdin
// never appears in `inspect`.
func (c *Client) Exec(ctx context.Context, containerID string, command []string, stdin io.Reader) (ExecResult, error) {
	var result ExecResult
	// A sidecar that does not answer must not hang the CLI for ever.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	created, err := c.API.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          command,
		AttachStdin:  stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return result, err
	}
	attached, err := c.API.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return result, err
	}
	defer attached.Close()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(attached.Conn, stdin)
			_ = attached.CloseWrite()
		}()
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil && err != io.EOF {
		return result, err
	}
	result.Stdout, result.Stderr = stdout.Bytes(), stderr.Bytes()

	result.ExitCode, err = c.waitExit(ctx, created.ID)
	return result, err
}

// exitWait is how long to wait for the engine to report that a command has finished.
var exitWait = 10 * time.Second

// waitExit returns the exit code of an exec. The engine only reports it once the process is gone, which
// can be a moment after its output ended: asking once would read "still running, code 0".
func (c *Client) waitExit(ctx context.Context, id string) (int, error) {
	deadline := time.Now().Add(exitWait)
	for {
		inspected, err := c.API.ContainerExecInspect(ctx, id)
		if err != nil {
			return 1, err
		}
		if !inspected.Running {
			return inspected.ExitCode, nil
		}
		if time.Now().After(deadline) {
			return 1, fmt.Errorf("exec did not finish")
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
