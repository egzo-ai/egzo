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

	// The exit code is only reported once the process is gone.
	deadline := time.Now().Add(10 * time.Second)
	for {
		inspected, err := c.API.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return result, err
		}
		if !inspected.Running {
			result.ExitCode = inspected.ExitCode
			return result, nil
		}
		if time.Now().After(deadline) {
			return result, fmt.Errorf("exec did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
