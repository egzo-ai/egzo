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
	"net/url"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/internal/termsafe"
)

// Version is the protocol's version. Before 1.0 a hub speaks one version and a link speaks one: there are no ranges.
const Version = 1

// Path is where a hub takes links.
const Path = "/link/v1"

// MaxLine is the longest message either side reads. What the other side says is untrusted.
const MaxLine = 4 << 20

const (
	// maxHello is the longest line of the handshake, read from anyone who reaches the endpoint. A Hello names a project and its templates.
	maxHello = 256 << 10
	// maxControl is the longest line a link sends on its control stream once it has proved its key: it has one thing left to say.
	maxControl = 4 << 10
)

// KeepAlive is how often an idle connection proves it is alive. Reverse proxies cut a silent connection (nginx at 60 seconds), and
// without it a path that went silent is not noticed at all.
const KeepAlive = 15 * time.Second

// The states of a registration, as a hub gives them.
const (
	StateUnregistered = "unregistered" // the hub does not know this key: `egzo project register` brings a code
	StateApproved     = "approved"
	StateRevoked      = "revoked"
	StateUnlinked     = "unlinked" // the hub has forgotten the project, as the link asked
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
	Error   string `json:",omitempty"`
}

// Text says it in a line, for people. Nearly all of it is what a hub said, which is untrusted, and it goes to a terminal.
func (s Status) Text() string { return termsafe.Line(s.text()) }

func (s Status) text() string {
	switch s.State {
	case StateApproved:
		return fmt.Sprintf("linked to %s as %s, owned by %s", s.Hub, s.Project, s.Owner)
	case StateUnregistered:
		return "not registered: run `egzo project register`"
	case StateRevoked:
		if s.Error != "" {
			return fmt.Sprintf("revoked by %s: %s", s.Hub, s.Error)
		}
		return "revoked by " + s.Hub
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

// The verbs a hub may ask. There is no other: a hub cannot run a container, mount a path or exec a command of its choosing. forward reaches
// a port an agent exposed, and no other: the link refuses the rest. files reaches the files of one of the project's workspace volumes, read
// only, through a helper the link makes (docs/hub.md, "Files").
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
	VerbForward   = "forward"
	// VerbWorkspaces lists the project's workspace volumes.
	VerbWorkspaces = "workspaces"
	// VerbFiles is a stream to the file server of one workspace volume: HTTP, GET only.
	VerbFiles = "files"
	// VerbSend is a message from one of the hub's users to an agent. Key makes it idempotent: the same key from the same user is the
	// same message, however often it is sent.
	VerbSend = "send"
	// VerbResolve is one of the hub's users answering a question, or closing a request, that is addressed to them. Key makes it
	// idempotent, as for send; the same key with another answer is an error.
	VerbResolve = "resolve"
	// VerbUndelivered is the hub saying that a message an agent sent one of its users did not reach them: Message, and Text, why, in words
	// for the agent.
	VerbUndelivered = "undelivered"
	// VerbPeople is the hub saying what it knows now of the users who started the project's agents: People, every one of them. An agent
	// whose starter became reachable, or unreachable, is told.
	VerbPeople = "people"
)

// The feeds a link pushes.
const (
	FeedProject = "project"
	FeedStatus  = "status"
	FeedAudit   = "audit"
	// FeedEvents is the control sidecar's event stream, from the sequence number the hub has (FeedStart.After). See Event.
	FeedEvents = "events"
)

// Hello is the link's first message.
type Hello struct {
	Protocol  int
	Project   string
	Host      string // the machine the engine runs on, as the engine says: shown to operators
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

// Proof is the link's signature over the challenge. Code is a link code, for a key the hub does not know yet: it comes after the
// protocol check, and the signature covers it, so it cannot be moved to another key. It is a secret: it is never logged.
type Proof struct {
	Signature []byte
	Code      string `json:",omitempty"`
}

// Verdict is where a registration stands. The hub sends one after the proof. A link that is not approved hears it and is closed, and an
// approved one hears another only if it is revoked or unlinked, as the last thing on the connection.
type Verdict struct {
	State   string
	Project string `json:",omitempty"` // the hub's id of the project
	Owner   string `json:",omitempty"`
	Message string `json:",omitempty"`
	// Preview is the template of a preview's address, with %s for an exposure's id, when the hub serves previews of this project. It holds
	// what only the hub knows: the scheme, the domain, the project.
	Preview string `json:",omitempty"`
}

const (
	// PreviewPlaceholder is where an exposure's id goes in a Verdict's Preview.
	PreviewPlaceholder = "%s"
	// MaxPreviewTemplate is the longest template taken.
	MaxPreviewTemplate = 256
)

// ValidPreviewTemplate takes a base address with one place for an id, in the host name: every preview is an origin of its own.
func ValidPreviewTemplate(template string) bool {
	if len(template) > MaxPreviewTemplate || strings.Count(template, "%") != 1 || strings.Count(template, PreviewPlaceholder) != 1 {
		return false
	}
	const id = "exposureid"
	u, err := url.Parse(strings.Replace(template, PreviewPlaceholder, id, 1))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && strings.Contains(u.Hostname(), id) && u.User == nil
}

// Unlink is the link's last message on the control stream: forget this project.
type Unlink struct {
	Unlink bool
}

// Request is the first line of a stream the hub opens.
type Request struct {
	Verb     string
	Template string `json:",omitempty"` // spawn
	Name     string `json:",omitempty"` // spawn: the instance's name, "" lets the project choose
	Actor    string `json:",omitempty"` // spawn: the `actor` label, who the agent is told started it
	// spawn: the starter's name, the task the agent starts on, and whether nobody reads its terminal (see SpawnRequest)
	StarterName string `json:",omitempty"`
	Task        string `json:",omitempty"`
	Unattended  bool   `json:",omitempty"`
	// people: by user:<id>
	People    map[string]Person `json:",omitempty"`
	Agent     string            `json:",omitempty"`
	Container string            `json:",omitempty"` // logs
	Tail      int               `json:",omitempty"` // logs
	Limit     int               `json:",omitempty"` // events
	Size      Size              `json:",omitempty"` // attach
	Port      int               `json:",omitempty"` // forward
	Workspace string            `json:",omitempty"` // files
	Instance  string            `json:",omitempty"` // files: the instance whose worktree, for a git workspace
	Text      string            `json:",omitempty"` // send, resolve; undelivered: why
	Key       string            `json:",omitempty"` // send, resolve: makes it idempotent, the platform's id of the message or the click
	Interrupt bool              `json:",omitempty"` // send
	Message   string            `json:",omitempty"` // resolve: the id of the question or request; undelivered: of the agent's message
	Outcome   string            `json:",omitempty"` // resolve: done, declined or failed; "" is done
}

// SpawnRequest is what the hub asks of a spawn. Actor is who starts the agent, and StarterName their name. Task is what the agent starts
// on: typed as its first prompt, as if they had typed it; or, Unattended, a request from them, and nobody reads its terminal. Unattended
// needs a task and a user:<id> actor.
type SpawnRequest struct {
	Template, Name, Actor, StarterName, Task string
	Unattended                               bool
}

// Person is what the hub says of one of its users who started agents of a project: their name, and whether a chat bridge can reach them
// now. Never which platform, nor why not.
type Person struct {
	Name      string
	Reachable bool
}

// Sent is what send answers: the message's id, the same however often the same key is sent.
type Sent struct {
	ID string
}

// Resolved is what resolve answers: the id of the resolution, the same however often the same key is sent. Already is true when the
// user had resolved the message before with another key, or none: nothing changed, and ID is their first resolution.
type Resolved struct {
	ID      string
	Already bool
}

// Reply is a line the link answers a Request with: progress of what it does, then one with Done. After a Done without Error,
// attach goes on as a terminal: raw output from the link, input frames from the hub. forward and files go on as raw bytes both ways, and
// their Done says the server was reached.
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

// FeedStart says where a feed begins: audit events after Since, or the end of the log's backlog when it is zero; events after After.
type FeedStart struct {
	Since time.Time
	// After is where the events feed begins: the events after this sequence number; negative, only those to come. The first event of a
	// feed that begins with those to come is Type "start", whose Seq says where that is: the hub keeps it as where it is, like any event.
	After int
}

// ProjectUpdate is a line of the project feed: the whole project each time it changed.
type ProjectUpdate struct {
	Project   Project
	Published Published
}

// Signed is what a link signs: the challenge and the link code it presents, if any, for this hub. A hub cannot pass a challenge of
// another hub on to its links and log in there, because the address is in what is signed, and a code signed for one key is no use to
// another.
func Signed(hub string, nonce []byte, key ed25519.PublicKey, code string) []byte {
	message := []byte(fmt.Sprintf("egzo link %d\n%s\n%d\n%s", Version, strings.TrimRight(hub, "/"), len(code), code))
	message = append(message, key...)
	return append(message, nonce...)
}

// Lines reads and writes one stream's messages.
type Lines struct {
	reader *bufio.Reader
	writer io.Writer
	limit  int
}

// NewLines wraps a stream.
func NewLines(stream io.ReadWriter) *Lines {
	return &Lines{reader: bufio.NewReaderSize(stream, 64<<10), writer: stream, limit: MaxLine}
}

// Read reads one message into v. A line over the limit, MaxLine unless the stream says less, ends the stream: nothing the protocol sends
// is that long.
func (l *Lines) Read(v any) error {
	var line []byte
	for {
		part, err := l.reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > l.limit {
			return fmt.Errorf("a line of over %d bytes: refusing the stream", l.limit)
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
