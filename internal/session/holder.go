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
	"syscall"
	"time"

	"github.com/creack/pty"
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
	rows, cols int

	done chan struct{}
	code int
}

type client struct {
	conn     net.Conn
	readOnly bool
	writeMu  sync.Mutex
}

func (c *client) send(kind byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.conn, kind, payload)
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
	h := &Holder{cmd: cmd, master: master, clients: map[*client]bool{}, rows: 24, cols: 80, done: make(chan struct{}), lastOutput: time.Now()}
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
	// Let the pump drain what the program wrote last.
	time.Sleep(100 * time.Millisecond)
	h.master.Close()
	body, _ := json.Marshal(exitStatus{Code: h.code})
	h.mu.Lock()
	for c := range h.clients {
		c.send(frameExit, body)
		c.conn.Close()
	}
	h.mu.Unlock()
	close(h.done)
}

// pump copies the program's output to every client.
func (h *Holder) pump() {
	buffer := make([]byte, 32<<10)
	for {
		n, err := h.master.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			h.mu.Lock()
			h.lastOutput = time.Now()
			h.tail = append(h.tail, chunk...)
			if len(h.tail) > tailSize {
				h.tail = h.tail[len(h.tail)-tailSize:]
			}
			clients := make([]*client, 0, len(h.clients))
			for c := range h.clients {
				clients = append(clients, c)
			}
			h.mu.Unlock()
			for _, c := range clients {
				if c.send(frameOutput, chunk) != nil {
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
	c.conn.Close()
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
	kind, payload, err := readFrame(conn)
	var greeting hello
	if err != nil || kind != frameHello || json.Unmarshal(payload, &greeting) != nil {
		conn.Close()
		return
	}
	c := &client{conn: conn, readOnly: greeting.ReadOnly}
	select {
	case <-h.done:
		body, _ := json.Marshal(exitStatus{Code: h.code})
		c.send(frameExit, body)
		conn.Close()
		return
	default:
	}
	// A client starts with what the program wrote last (what the user would otherwise miss), and
	// no new output may overtake it, so the client is registered and its history sent under its
	// write lock. The program is then asked to repaint at the client's size.
	c.writeMu.Lock()
	h.mu.Lock()
	history := append([]byte(nil), h.tail...)
	h.clients[c] = true
	h.mu.Unlock()
	if len(history) > 0 {
		writeFrame(c.conn, frameOutput, history)
	}
	c.writeMu.Unlock()
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
			h.master.Write(payload)
		case frameResize:
			var s size
			if json.Unmarshal(payload, &s) == nil && !c.readOnly {
				h.resize(s.Rows, s.Cols)
			}
		}
	}
}

func (h *Holder) resize(rows, cols int) {
	if rows <= 0 || cols <= 0 || rows > 1000 || cols > 1000 {
		return
	}
	h.mu.Lock()
	h.rows, h.cols = rows, cols
	h.mu.Unlock()
	pty.Setsize(h.master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// repaint makes the program draw its screen for a client that just arrived: the program is asked
// to redraw by a window size change, with a real change when the size differs and a brief
// one-column wobble when it does not (the kernel only signals on a change).
func (h *Holder) repaint(rows, cols int) {
	if rows <= 0 || cols <= 0 {
		rows, cols = h.rows, h.cols
	}
	h.mu.Lock()
	same := rows == h.rows && cols == h.cols
	h.mu.Unlock()
	if same && cols > 1 {
		pty.Setsize(h.master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols - 1)})
		time.Sleep(30 * time.Millisecond)
	}
	h.resize(rows, cols)
}

// Inject types text into the program as one bracketed paste followed by Enter.
func (h *Holder) Inject(text string) error {
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
	_, err := io.WriteString(h.master, data)
	return err
}

// LastHumanInput is when a read-write client last typed; the zero time when none has.
func (h *Holder) LastHumanInput() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastHuman
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
	return append([]byte(nil), h.tail...)
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
