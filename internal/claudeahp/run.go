// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/internal/session"
)

// Options is what Run serves.
type Options struct {
	// Session is the main session's id, and Transcript where Claude Code keeps it (it may not be there yet).
	Session    string
	Transcript string
	// Listen is the address to serve on (the container's loopback), Path the AHP WebSocket's, HookPath the hooks'.
	Listen   string
	Path     string
	HookPath string
	// Token is what clients and hooks present.
	Token string
	// Socket is the session holder's, to type into the TUI; ReadyMarkers what the TUI draws once it takes a prompt.
	Socket       string
	ReadyMarkers []string
	InterruptKey string
	Version      string
}

// Run serves the main session's chat until ctx ends.
func Run(ctx context.Context, o Options) error {
	tui := &TUI{Socket: o.Socket, Markers: o.ReadyMarkers, InterruptKey: o.InterruptKey}
	defer tui.Close()
	host := New(Config{Session: o.Session, Typer: tui, Version: o.Version})
	follow := &Tail{Path: o.Transcript, Apply: host.Apply}
	host.drain = follow.Drain
	host.onTranscript = follow.SetPath
	follow.Drain() // the chat so far, before anybody sees it

	listener, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", o.Listen, err)
	}
	server := &http.Server{Handler: host.Handler(o.Token, o.Path, o.HookPath), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				server.Close()
				return
			case <-ticker.C:
				follow.Drain()
			}
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Tail follows a transcript: Drain reads what was added since, and applies each complete line.
type Tail struct {
	Path  string
	Apply func(line []byte)

	mu      sync.Mutex
	offset  int64
	partial []byte
}

// SetPath follows another file, as long as nothing was read of the first one: the transcript Claude Code says it writes (a hook's
// transcript_path) wins over the one the adapter expected.
func (t *Tail) SetPath(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if path == "" || path == t.Path || t.offset > 0 {
		return
	}
	t.Path = path
}

// Drain reads the transcript to its end.
func (t *Tail) Drain() {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.Open(t.Path)
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() < t.offset {
		// replaced or cut: start over (the chat keeps what it had, and the turns it reads again are new ones)
		t.offset, t.partial = 0, nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil || len(data) == 0 {
		return
	}
	t.offset += int64(len(data))
	data = append(t.partial, data...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(data[:i])
		data = data[i+1:]
		if len(line) > 0 {
			t.Apply(line)
		}
	}
	t.partial = append([]byte{}, data...)
}

// TUI types into Claude Code's terminal through the session holder, as a person would.
type TUI struct {
	Socket       string
	Markers      []string
	InterruptKey string

	mu     sync.Mutex
	remote *session.Remote
	ready  bool
}

func (t *TUI) dial() (*session.Remote, error) {
	if t.remote != nil && t.remote.Alive() {
		return t.remote, nil
	}
	if t.remote != nil {
		t.remote.Close()
	}
	r, err := session.DialRemote(t.Socket, 5*time.Second)
	if err != nil {
		return nil, err
	}
	t.remote = r
	return r, nil
}

// readyWait is how long a prompt waits for the TUI to show it takes one.
const readyWait = 90 * time.Second

// Prompt types text as one paste, and Enter.
func (t *TUI) Prompt(text string, idle bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, err := t.dial()
	if err != nil {
		return err
	}
	if idle && !t.ready && len(t.Markers) > 0 {
		deadline := time.Now().Add(readyWait)
		for !drawn(r.Output(), t.Markers) {
			if time.Now().After(deadline) || !r.Alive() {
				break // a TUI whose screen changed must not strand the prompt: type it anyway
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.ready = true
		time.Sleep(500 * time.Millisecond)
	}
	if err := r.Write([]byte("\x1b[200~" + session.Sanitize(text) + "\x1b[201~")); err != nil {
		return err
	}
	time.Sleep(250 * time.Millisecond)
	return r.Write([]byte("\r"))
}

// Interrupt presses Esc.
func (t *TUI) Interrupt() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, err := t.dial()
	if err != nil {
		return err
	}
	key := t.InterruptKey
	if key == "" {
		key = "\x1b"
	}
	return r.Write([]byte(key))
}

// Close disconnects from the holder.
func (t *TUI) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.remote != nil {
		t.remote.Close()
	}
}

func drawn(output []byte, markers []string) bool {
	text := string(output)
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}
