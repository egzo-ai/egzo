// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// SocketPath is where the holder listens inside the agent container.
func SocketPath() string {
	if path := os.Getenv("EGZO_SESSION_SOCKET"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "egzo-session", "agent.sock")
}

// Holder runs one program on a pty and serves attach clients.
type Holder struct {
	cmd    *exec.Cmd
	master *os.File

	mu         sync.Mutex
	clients    map[*client]bool
	lastHuman  time.Time // the last time a read-write client typed
	lastOutput time.Time
	tail       []byte // the program's most recent output, for acknowledging injected text
	wrote      bool
	rows, cols int

	// inputMu keeps what is typed into the program in one piece: a client's keys and an injected
	// paste with its Enter are never interleaved.
	inputMu sync.Mutex

	helloTimeout atomic.Int64 // nanoseconds

	pumped chan struct{} // closed when the program's output has been read to the end
	done   chan struct{}
	code   int
}

// defaultHelloTimeout is how long a new connection may take to say who it is.
const defaultHelloTimeout = 10 * time.Second

// clientWriteTimeout is how long one frame may take to reach a client before the client is dropped.
const clientWriteTimeout = 10 * time.Second

// clientQueue is how many frames may wait for a slow client. The program's output is never held back
// for a client: one that falls this far behind (a hung terminal, a stalled exec stream) is dropped, and
// can attach again to get the screen back.
const clientQueue = 256

type frame struct {
	kind    byte
	payload []byte
}

type client struct {
	conn     net.Conn
	readOnly bool

	mu     sync.Mutex
	closed bool
	out    chan frame
}

func newClient(conn net.Conn, readOnly bool) *client {
	c := &client{conn: conn, readOnly: readOnly, out: make(chan frame, clientQueue)}
	go c.writer()
	return c
}

// enqueue hands a frame to the client's writer without ever blocking; false means the client is gone or
// too far behind.
func (c *client) enqueue(kind byte, payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.out <- frame{kind, payload}:
		return true
	default:
		return false
	}
}

// finish lets the writer send what is queued, then closes the connection.
func (c *client) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.out)
	}
}

// kill closes the connection at once, discarding what is queued.
func (c *client) kill() {
	c.finish()
	c.conn.Close()
}

func (c *client) writer() {
	defer c.conn.Close()
	for f := range c.out {
		c.conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
		if writeFrame(c.conn, f.kind, f.payload) != nil {
			c.kill()
			for range c.out { // let the queue drain so nothing waits on it
			}
			return
		}
	}
}

const tailSize = 64 << 10

// Start runs argv on a new pty with the given environment.
func Start(argv, env []string, dir string) (*Holder, error) {
	if len(argv) == 0 {
		return nil, errors.New("no program to run")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	h := &Holder{cmd: cmd, master: master, clients: map[*client]bool{}, rows: 24, cols: 80, pumped: make(chan struct{}), done: make(chan struct{}), lastOutput: time.Now()}
	h.helloTimeout.Store(int64(defaultHelloTimeout))
	go h.pump()
	go h.wait()
	return h, nil
}

// Done is closed when the program has exited; ExitCode is valid then.
func (h *Holder) Done() <-chan struct{} { return h.done }

func (h *Holder) ExitCode() int { return h.code }

func (h *Holder) wait() {
	err := h.cmd.Wait()
	h.code = 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		h.code = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			h.code = 128 + int(status.Signal())
		}
	}
	// Let the pump read what the program wrote last: it ends when the terminal reports that nobody
	// holds the other end any more. A program that left a child holding it must not hold us up.
	select {
	case <-h.pumped:
	case <-time.After(2 * time.Second):
	}
	h.master.Close()
	body, _ := json.Marshal(exitStatus{Code: h.code})
	h.mu.Lock()
	for c := range h.clients {
		c.enqueue(frameExit, body)
		c.finish()
	}
	h.clients = map[*client]bool{}
	h.mu.Unlock()
	close(h.done)
}

// pump copies the program's output to every client.
func (h *Holder) pump() {
	defer close(h.pumped)
	buffer := make([]byte, 32<<10)
	for {
		n, err := h.master.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			h.mu.Lock()
			h.lastOutput = time.Now()
			h.wrote = true
			h.tail = append(h.tail, chunk...)
			if len(h.tail) > 2*tailSize {
				// trimmed in one step per tailSize bytes written, not on every chunk
				h.tail = h.tail[:copy(h.tail, h.tail[len(h.tail)-tailSize:])]
			}
			clients := make([]*client, 0, len(h.clients))
			for c := range h.clients {
				clients = append(clients, c)
			}
			h.mu.Unlock()
			for _, c := range clients {
				if !c.enqueue(frameOutput, chunk) {
					h.drop(c)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *Holder) drop(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.kill()
}

// Serve accepts attach clients until the listener closes.
func (h *Holder) Serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go h.handle(conn)
	}
}

func (h *Holder) handle(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(time.Duration(h.helloTimeout.Load())))
	kind, payload, err := readFrame(conn)
	var greeting hello
	if err != nil || kind != frameHello || json.Unmarshal(payload, &greeting) != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	c := newClient(conn, greeting.ReadOnly)
	select {
	case <-h.done:
		body, _ := json.Marshal(exitStatus{Code: h.code})
		c.enqueue(frameExit, body)
		c.finish()
		return
	default:
	}
	// A client starts with what the program wrote last (what the user would otherwise miss), and no
	// new output may overtake it: the history is queued before the client is registered, under the
	// lock the pump takes to add output, so every byte is either in the history or sent after it.
	h.mu.Lock()
	if recent := h.recent(); len(recent) > 0 {
		c.enqueue(frameOutput, recent)
	}
	h.clients[c] = true
	h.mu.Unlock()
	h.repaint(greeting.Rows, greeting.Cols)

	defer h.drop(c)
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case frameInput:
			if c.readOnly {
				continue
			}
			h.mu.Lock()
			h.lastHuman = time.Now()
			h.mu.Unlock()
			h.inputMu.Lock()
			h.master.Write(payload)
			h.inputMu.Unlock()
		case frameResize:
			var s size
			if json.Unmarshal(payload, &s) == nil && !c.readOnly {
				h.resize(s.Rows, s.Cols)
			}
		}
	}
}

// setsize tells the terminal its size. It goes through the file's own syscall conn, which is safe
// against the file being closed at the same time (Fd() is not: the number could be reused).
func (h *Holder) setsize(rows, cols int) {
	conn, err := h.master.SyscallConn()
	if err != nil {
		return
	}
	conn.Control(func(fd uintptr) {
		unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	})
}

func (h *Holder) resize(rows, cols int) {
	if rows <= 0 || cols <= 0 || rows > 1000 || cols > 1000 {
		return
	}
	h.mu.Lock()
	h.rows, h.cols = rows, cols
	h.mu.Unlock()
	h.setsize(rows, cols)
}

// repaint makes the program draw its screen for a client that just arrived: the program is asked
// to redraw by a window size change, with a real change when the size differs and a brief
// one-column wobble when it does not (the kernel only signals on a change).
func (h *Holder) repaint(rows, cols int) {
	h.mu.Lock()
	if rows <= 0 || cols <= 0 {
		rows, cols = h.rows, h.cols
	}
	if rows > 1000 || cols > 1000 {
		rows, cols = h.rows, h.cols
	}
	same := rows == h.rows && cols == h.cols
	h.mu.Unlock()
	if same && cols > 1 {
		h.setsize(rows, cols-1)
		time.Sleep(30 * time.Millisecond)
	}
	h.resize(rows, cols)
}

// Inject types text into the program as one bracketed paste followed by Enter.
func (h *Holder) Inject(text string) error {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	paste := "\x1b[200~" + text + "\x1b[201~"
	if _, err := io.WriteString(h.master, paste); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	_, err := io.WriteString(h.master, "\r")
	return err
}

// Send writes raw bytes to the program, such as the harness's interrupt key.
func (h *Holder) Send(data string) error {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	_, err := io.WriteString(h.master, data)
	return err
}

// LastHumanInput is when a read-write client last typed; the zero time when none has.
func (h *Holder) LastHumanInput() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastHuman
}

// Raw reports whether the program has taken the terminal over (turned off line editing), which is
// what a TUI does when it starts. Input sent before that is discarded when the mode changes.
func (h *Holder) Raw() bool {
	conn, err := h.master.SyscallConn()
	if err != nil {
		return false
	}
	raw := false
	conn.Control(func(fd uintptr) {
		if termios, err := unix.IoctlGetTermios(int(fd), unix.TCGETS); err == nil {
			raw = termios.Lflag&unix.ICANON == 0
		}
	})
	return raw
}

// Wrote reports whether the program has written anything yet.
func (h *Holder) Wrote() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tail) > 0 || h.wrote
}

// LastOutput is when the program last wrote anything.
func (h *Holder) LastOutput() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastOutput
}

// Output returns the program's most recent output.
func (h *Holder) Output() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recent()
}

// recent is a copy of the last tailSize bytes the program wrote. It runs under h.mu.
func (h *Holder) recent() []byte {
	tail := h.tail
	if len(tail) > tailSize {
		tail = tail[len(tail)-tailSize:]
	}
	return append([]byte(nil), tail...)
}

// Terminate asks the program to stop, then makes sure it does.
func (h *Holder) Terminate() {
	if h.cmd.Process == nil {
		return
	}
	h.cmd.Process.Signal(syscall.SIGHUP)
	h.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		h.cmd.Process.Kill()
		<-h.done
	}
}

// Listen opens the holder's socket, readable only by the user it runs as.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return listener, os.Chmod(path, 0o600)
}

// ForwardSignals ends the program when the holder is told to stop.
func (h *Holder) ForwardSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-signals
		h.Terminate()
	}()
}
