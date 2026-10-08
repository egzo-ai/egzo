// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package session is egzo's session backend: a pty holder that runs a harness TUI on a terminal
// inside the agent container, and the clients that attach to it.
//
// The holder is a raw pass-through: bytes the program writes go to every client untouched, bytes a
// read-write client types go to the program untouched. It adds no screen, no status bar and no key
// handling (the detach key is the one exception, and lives in the client). That is what keeps the
// harness's own TUI exactly what the user knows.
package session

import (
	"encoding/binary"
	"errors"
	"io"
)

// Frame types. A frame is one type byte, a four byte big-endian length and the payload.
const (
	frameHello  byte = 'H' // client -> holder: JSON hello
	frameInput  byte = 'I' // client -> holder: bytes typed
	frameResize byte = 'R' // client -> holder: JSON size
	frameOutput byte = 'O' // holder -> client: bytes the program wrote
	frameExit   byte = 'X' // holder -> client: JSON exit status, the last frame
	frameError  byte = 'E' // holder -> client: text, the last frame
)

const maxFrame = 1 << 20

type hello struct {
	ReadOnly bool `json:"read_only"`
	Rows     int  `json:"rows"`
	Cols     int  `json:"cols"`
}

type size struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type exitStatus struct {
	Code int `json:"code"`
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	header := make([]byte, 5, 5+len(payload))
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	// One write, so frames from different goroutines never interleave when callers hold a lock.
	_, err := w.Write(append(header, payload...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFrame {
		return 0, nil, errors.New("frame too large")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}
