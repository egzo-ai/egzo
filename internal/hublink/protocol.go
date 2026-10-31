// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package hublink is the protocol between a project's link and a hub, and the hub's side of it. A link holds one outbound WebSocket
// to the hub, carrying yamux. It opens one control stream, which it starts with the handshake, and one stream per feed. The hub
// opens one stream per verb. Every message is a line of JSON; a terminal's bytes follow its verb's reply on the same stream.
//
// This package needs no engine: core's link (internal/link) and the hub both import it, and the hub stays free of the Docker client.
package hublink

import (
	"bufio"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Version is the protocol's version. Before 1.0 a hub speaks one version and a link speaks one: there are no ranges.
const Version = 1

// Path is where a hub takes links.
const Path = "/link/v1"

// MaxLine is the longest message either side reads. What the other side says is untrusted.
const MaxLine = 4 << 20

// KeepAlive is how often an idle connection proves it is alive. Reverse proxies cut a silent connection (nginx at 60 seconds), and
// without it a path that went silent is not noticed at all.
const KeepAlive = 15 * time.Second

// The states of a registration, as a hub gives them.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateRefused  = "refused"
	StateRevoked  = "revoked"
	StateUnlinked = "unlinked" // the hub has forgotten the project, as the link asked
)

// The states of a link that are not a registration's.
const (
	StateLocked      = "locked"      // the secret key has not come yet: `egzo project up` brings it
	StateConnecting  = "connecting"  // the first answer has not come yet
	StateUnreachable = "unreachable" // the last try failed; Error says why, and the link keeps trying
	StateNoEngine    = "no engine"   // the engine's socket cannot be reached from the sidecar
)

// Status is what a link knows of itself: `egzo ps` and `egzo project check` show it.
type Status struct {
	Hub     string
	State   string
	Project string `json:",omitempty"` // the hub's id
	Owner   string `json:",omitempty"`
	Code    string `json:",omitempty"`
	Address string `json:",omitempty"`
	Error   string `json:",omitempty"`
}

// Text says it in a line, for people.
func (s Status) Text() string {
	switch s.State {
	case StateApproved:
		return fmt.Sprintf("linked to %s as %s, owned by %s", s.Hub, s.Project, s.Owner)
	case StatePending:
		return "pending: register this project at " + s.Address
	case StateRefused, StateRevoked:
		if s.Error != "" {
			return fmt.Sprintf("%s by %s: %s", s.State, s.Hub, s.Error)
		}
		return fmt.Sprintf("%s by %s", s.State, s.Hub)
	case StateLocked:
		if s.Error != "" {
			return "locked: " + s.Error
		}
		return "locked: run `egzo project up`, which brings the secret key"
	case StateUnreachable, StateNoEngine:
		return s.State + ": " + s.Error
	}
	return s.State
}

// The verbs a hub may ask. There is no other: a hub cannot run a container, mount a path or exec a command of its choosing.
const (
	VerbProject   = "project"
	VerbPublished = "published"
	VerbSpawn     = "spawn"
	VerbStart     = "start"
	VerbStop      = "stop"
	VerbRemove    = "remove"
	VerbAttach    = "attach"
	VerbEvents    = "events"
	VerbLogs      = "logs"
	VerbSpec      = "spec"
)

// The feeds a link pushes.
const (
	FeedProject = "project"
	FeedStatus  = "status"
	FeedAudit   = "audit"
)

// Hello is the link's first message.
type Hello struct {
	Protocol  int
	Project   string
	Host      string // the machine the engine runs on, as the engine says: shown to the person who approves
	Release   string // this egzo's version, for people
	Key       ed25519.PublicKey
	Templates []string
}

// Greeting answers a Hello: a challenge, or why the hub will not talk to this link.
type Greeting struct {
	Protocol int
	Nonce    []byte `json:",omitempty"`
	Refused  string `json:",omitempty"`
}

// Proof is the link's signature over the challenge.
type Proof struct {
	Signature []byte
}

// Verdict is where a registration stands. The hub sends one after the proof and another each time it changes.
type Verdict struct {
	State   string
	Project string `json:",omitempty"` // the hub's id of the project
	Owner   string `json:",omitempty"`
	Code    string `json:",omitempty"` // pending: the device code
	Address string `json:",omitempty"` // pending: where a person approves it
	Message string `json:",omitempty"`
}

// Unlink is the link's last message on the control stream: forget this project.
type Unlink struct {
	Unlink bool
}

// Request is the first line of a stream the hub opens.
type Request struct {
	Verb      string
	Template  string `json:",omitempty"` // spawn
	Name      string `json:",omitempty"` // spawn: the instance's name, "" lets the project choose
	Actor     string `json:",omitempty"` // spawn: the `actor` label
	Agent     string `json:",omitempty"`
	Container string `json:",omitempty"` // logs
	Tail      int    `json:",omitempty"` // logs
	Limit     int    `json:",omitempty"` // events
	Size      Size   `json:",omitempty"` // attach
}

// Reply is a line the link answers a Request with: progress of what it does, then one with Done. After a Done without Error,
// attach goes on as a terminal: raw output from the link, input frames from the hub.
type Reply struct {
	Progress string          `json:",omitempty"`
	Done     bool            `json:",omitempty"`
	Error    string          `json:",omitempty"`
	Gone     bool            `json:",omitempty"` // the error is that the project is gone
	Result   json.RawMessage `json:",omitempty"`
}

// FeedOpen is the first line of a stream the link opens for a feed; FeedStart answers it.
type FeedOpen struct {
	Feed string
}

// FeedStart says where a feed begins: audit events after Since, or the end of the log's backlog when it is zero.
type FeedStart struct {
	Since time.Time
}

// ProjectUpdate is a line of the project feed: the whole project each time it changed.
type ProjectUpdate struct {
	Project   Project
	Published Published
}

// Signed is what a link signs: the challenge, for this hub. A hub cannot pass a challenge of another hub on to its links and
// log in there, because the address is in what is signed.
func Signed(hub string, nonce []byte, key ed25519.PublicKey) []byte {
	message := []byte(fmt.Sprintf("egzo link %d\n%s\n", Version, strings.TrimRight(hub, "/")))
	message = append(message, key...)
	return append(message, nonce...)
}

// Lines reads and writes one stream's messages.
type Lines struct {
	reader *bufio.Reader
	writer io.Writer
}

// NewLines wraps a stream.
func NewLines(stream io.ReadWriter) *Lines {
	return &Lines{reader: bufio.NewReaderSize(stream, 64<<10), writer: stream}
}

// Read reads one message into v. A line over MaxLine ends the stream: nothing the protocol sends is that long.
func (l *Lines) Read(v any) error {
	var line []byte
	for {
		part, err := l.reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > MaxLine {
			return fmt.Errorf("a line of over %d bytes: refusing the stream", MaxLine)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(line, v)
	}
}

// Write writes one message.
func (l *Lines) Write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = l.writer.Write(append(data, '\n'))
	return err
}

// Rest is what was read past the last line, then the stream: where a terminal's bytes begin.
func (l *Lines) Rest() io.Reader { return l.reader }

// A terminal's input is frames: a type, a length, and that many bytes.
const (
	frameInput  byte = 'i'
	frameResize byte = 'r'
	// maxFrame is the longest input frame: a paste is cut into pieces of this size.
	maxFrame = 32 << 10
)

// WriteInput sends what the user typed.
func WriteInput(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), maxFrame)
		if err := writeFrame(w, frameInput, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// WriteResize sends a terminal's new size.
func WriteResize(w io.Writer, size Size) error {
	data, _ := json.Marshal(size)
	return writeFrame(w, frameResize, data)
}

func writeFrame(w io.Writer, kind byte, data []byte) error {
	frame := make([]byte, 5, 5+len(data))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(data)))
	_, err := w.Write(append(frame, data...))
	return err
}

// ReadFrames reads a terminal's input until the stream ends: bytes go to in, sizes to resize.
func ReadFrames(r io.Reader, in io.Writer, resize func(Size)) error {
	var header [5]byte
	buffer := make([]byte, maxFrame)
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(header[1:])
		if size > maxFrame {
			return fmt.Errorf("an input frame of %d bytes: refusing the stream", size)
		}
		if _, err := io.ReadFull(r, buffer[:size]); err != nil {
			return err
		}
		switch header[0] {
		case frameInput:
			if _, err := in.Write(buffer[:size]); err != nil {
				return err
			}
		case frameResize:
			var s Size
			if json.Unmarshal(buffer[:size], &s) == nil {
				resize(s)
			}
		default:
			return fmt.Errorf("an input frame of type %d: refusing the stream", header[0])
		}
	}
}
