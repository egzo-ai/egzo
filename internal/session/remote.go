// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Remote is a read-write client of the holder for a program of the container rather than a person: it types into the harness's terminal,
// and keeps the end of what the program draws, to see when it takes input. It has no size: the terminal keeps the size people gave it.
// What it types counts as a person typing (Delivery.HumanQuiet): it types for one, from another screen.
type Remote struct {
	conn net.Conn
	mu   sync.Mutex
	out  []byte
	done chan struct{}
	err  error
}

// remoteKeep is how much of the program's output a Remote keeps.
const remoteKeep = 64 << 10

// DialRemote connects to the holder at socket.
func DialRemote(socket string, timeout time.Duration) (*Remote, error) {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, err
	}
	greeting, _ := json.Marshal(hello{ReadOnly: false})
	if err := writeFrame(conn, frameHello, greeting); err != nil {
		conn.Close()
		return nil, err
	}
	r := &Remote{conn: conn, done: make(chan struct{})}
	go r.read()
	return r, nil
}

func (r *Remote) read() {
	defer close(r.done)
	for {
		kind, payload, err := readFrame(r.conn)
		if err != nil {
			r.mu.Lock()
			r.err = err
			r.mu.Unlock()
			return
		}
		switch kind {
		case frameOutput:
			r.mu.Lock()
			r.out = append(r.out, payload...)
			if len(r.out) > remoteKeep {
				r.out = append([]byte{}, r.out[len(r.out)-remoteKeep:]...)
			}
			r.mu.Unlock()
		case frameExit, frameError:
			r.mu.Lock()
			r.err = errors.New("the harness exited")
			r.mu.Unlock()
			return
		}
	}
}

// Output is the end of what the program drew since the Remote connected (with what it drew last before).
func (r *Remote) Output() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte{}, r.out...)
}

// Alive says whether the connection still holds.
func (r *Remote) Alive() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// Write types data into the program.
func (r *Remote) Write(data []byte) error {
	if !r.Alive() {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.err
	}
	r.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return writeFrame(r.conn, frameInput, data)
}

// Close disconnects.
func (r *Remote) Close() error { return r.conn.Close() }
