package engine

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// Stream describes how a command's input and output are wired to the caller.
type Stream struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// TTY gives the command a terminal: output is one raw stream and the window size matters.
	TTY bool
	// Size reports the caller's terminal size, for TTY commands.
	Size func() (width, height uint)
}

// ExecStream runs command in a container with its streams connected to the caller, and returns the
// command's exit code. Resizes of the caller's terminal are applied through Resize.
func (c *Client) ExecStream(ctx context.Context, containerID string, command []string, s Stream, resized <-chan struct{}) (int, error) {
	created, err := c.API.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          command,
		AttachStdin:  s.In != nil,
		AttachStdout: true,
		AttachStderr: !s.TTY,
		Tty:          s.TTY,
	})
	if err != nil {
		return 1, err
	}
	attached, err := c.API.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: s.TTY})
	if err != nil {
		return 1, err
	}
	defer attached.Close()

	resize := func() {
		if s.TTY && s.Size != nil {
			width, height := s.Size()
			_ = c.API.ContainerExecResize(ctx, created.ID, container.ResizeOptions{Width: width, Height: height})
		}
	}
	resize()
	if resized != nil {
		go func() {
			for range resized {
				resize()
			}
		}()
	}

	if s.In != nil {
		go func() {
			_, _ = io.Copy(attached.Conn, s.In)
			_ = attached.CloseWrite()
		}()
	}
	if s.TTY {
		_, err = io.Copy(s.Out, attached.Reader)
	} else {
		_, err = stdcopy.StdCopy(s.Out, s.Err, attached.Reader)
	}
	if err != nil && err != io.EOF {
		return 1, err
	}

	inspected, err := c.API.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return 1, err
	}
	return inspected.ExitCode, nil
}
