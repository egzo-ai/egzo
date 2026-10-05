package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// DetachKeys parses a key like "ctrl-]" into the byte a terminal sends for it.
func DetachKeys(spec string) (byte, error) {
	if len(spec) == 6 && spec[:5] == "ctrl-" {
		key := spec[5]
		switch {
		case key >= 'a' && key <= 'z':
			return key - 'a' + 1, nil
		case key >= 'A' && key <= 'Z':
			return key - 'A' + 1, nil
		case key == '[':
			return 0x1b, nil
		case key == '\\':
			return 0x1c, nil
		case key == ']':
			return 0x1d, nil
		case key == '^':
			return 0x1e, nil
		case key == '_':
			return 0x1f, nil
		}
	}
	return 0, fmt.Errorf("unsupported detach key %q (use ctrl-<letter>, ctrl-], ctrl-\\, ctrl-^ or ctrl-_)", spec)
}

// Attach connects the terminal on stdin and stdout to the holder's session until the program exits
// (its exit code is returned) or the detach key is typed (0).
func Attach(socket string, readOnly bool, detach byte, in *os.File, out io.Writer) (int, error) {
	code, err := attach(socket, readOnly, detach, in, out)
	// The program can no longer turn off what it turned on (mouse tracking, bracketed paste, focus
	// events, the keyboard protocol, a hidden cursor): leave the user's terminal as we found it. Only for
	// a terminal; a pipe gets exactly the program's bytes.
	if term.IsTerminal(int(in.Fd())) {
		io.WriteString(out, resetTerminal)
	}
	return code, err
}

// resetTerminal turns off the terminal modes a TUI commonly enables and shows the cursor again.
const resetTerminal = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[<u\x1b[?25h"

func attach(socket string, readOnly bool, detach byte, in *os.File, out io.Writer) (int, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return 0, fmt.Errorf("no session in this container (is the agent's harness running through `egzo agent run`?): %w", err)
	}
	defer conn.Close()

	rows, cols := 24, 80
	if width, height, err := term.GetSize(int(in.Fd())); err == nil {
		rows, cols = height, width
	}
	body, _ := json.Marshal(hello{ReadOnly: readOnly, Rows: rows, Cols: cols})
	if err := writeFrame(conn, frameHello, body); err != nil {
		return 0, err
	}

	if term.IsTerminal(int(in.Fd())) {
		state, err := term.MakeRaw(int(in.Fd()))
		if err != nil {
			return 0, err
		}
		defer term.Restore(int(in.Fd()), state)
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	detached := make(chan struct{})
	go func() {
		for range winch {
			if width, height, err := term.GetSize(int(in.Fd())); err == nil {
				body, _ := json.Marshal(size{Rows: height, Cols: width})
				writeFrame(conn, frameResize, body)
			}
		}
	}()
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := in.Read(buffer)
			if n > 0 {
				chunk := buffer[:n]
				for i, b := range chunk {
					if b == detach {
						if i > 0 {
							writeFrame(conn, frameInput, chunk[:i])
						}
						close(detached)
						return
					}
				}
				if writeFrame(conn, frameInput, chunk) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	frames := make(chan struct {
		kind    byte
		payload []byte
		err     error
	})
	go func() {
		for {
			kind, payload, err := readFrame(conn)
			frames <- struct {
				kind    byte
				payload []byte
				err     error
			}{kind, payload, err}
			if err != nil || kind == frameExit || kind == frameError {
				return
			}
		}
	}()
	for {
		select {
		case <-detached:
			return 0, nil
		case frame := <-frames:
			if frame.err != nil {
				if errors.Is(frame.err, io.EOF) {
					return 0, nil
				}
				return 0, frame.err
			}
			switch frame.kind {
			case frameOutput:
				out.Write(frame.payload)
			case frameExit:
				var status exitStatus
				json.Unmarshal(frame.payload, &status)
				return status.Code, nil
			case frameError:
				return 1, errors.New(string(frame.payload))
			}
		}
	}
}
