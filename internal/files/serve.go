// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
)

// Socket is where the server listens in its helper container.
const Socket = "/tmp/egzo-files.sock"

// Connected is the line `egzo files connect --announce` writes on stderr once it reached the server, as `egzo agent forward` does.
const Connected = "connected"

// ServeOptions bound the server.
type ServeOptions struct {
	// Idle is how long the server waits with no connection before it exits, which ends its helper container.
	Idle time.Duration
	// MaxConns is the most connections served at once; the next one waits.
	MaxConns int
}

// Serve serves the files under dir on a unix socket until it has had no connection for options.Idle, or ctx ends.
func Serve(ctx context.Context, dir, socket string, options ServeOptions) error {
	root, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Stat(); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	_ = os.Remove(socket) // a socket left by a server that was killed
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if options.MaxConns <= 0 {
		options.MaxConns = 32
	}
	idle := newIdleTimer(options.Idle)
	server := &http.Server{
		Handler:           Handler{Root: root},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				idle.open()
			case http.StateClosed, http.StateHijacked:
				idle.close()
			}
		},
	}
	stopped := make(chan error, 1)
	go func() {
		stopped <- server.Serve(&limitedListener{Listener: listener, slots: make(chan struct{}, options.MaxConns)})
	}()
	select {
	case err := <-stopped:
		return err
	case <-idle.expired:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// idleTimer fires once nothing has been open for its duration.
type idleTimer struct {
	mu        sync.Mutex
	openConns int
	after     time.Duration
	timer     *time.Timer
	expired   chan struct{}
	once      sync.Once
}

func newIdleTimer(after time.Duration) *idleTimer {
	if after <= 0 {
		after = 5 * time.Minute
	}
	t := &idleTimer{after: after, expired: make(chan struct{})}
	t.timer = time.AfterFunc(after, t.fire)
	return t
}

func (t *idleTimer) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openConns == 0 {
		t.once.Do(func() { close(t.expired) })
	}
}

func (t *idleTimer) open() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.openConns++
	t.timer.Stop()
}

func (t *idleTimer) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openConns > 0 {
		t.openConns--
	}
	if t.openConns == 0 {
		t.timer.Reset(t.after)
	}
}

// limitedListener accepts at most cap(slots) connections at a time.
type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &slotConn{Conn: conn, release: func() { <-l.slots }}, nil
}

type slotConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *slotConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

// connectWait is how long Connect waits for a server that is starting: the helper container may have just been made.
var connectWait = 10 * time.Second

// Connect carries one connection between in and out and the server on socket, until either side ends. The end of in ends it all: the
// client is the hub's HTTP proxy, which never half closes. connected, when not nil, is called once the server has been reached.
func Connect(socket string, in io.Reader, out io.Writer, connected func()) error {
	conn, err := dialWaiting(socket, connectWait)
	if err != nil {
		return err
	}
	defer conn.Close()
	if connected != nil {
		connected()
	}
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.Close()
	}()
	_, err = io.Copy(out, conn)
	if err != nil && (errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)) {
		return nil
	}
	return err
}

func dialWaiting(socket string, wait time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(wait)
	for {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			return conn, nil
		}
		starting := errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
		if !starting || time.Now().After(deadline) {
			return nil, fmt.Errorf("the file server does not answer: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
