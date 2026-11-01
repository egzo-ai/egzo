// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Peer is a link, seen from the hub, once it has proved its key. It is the hub's way to everything on the project: its verbs are
// what the hub's Engine seam calls.
type Peer struct {
	Hello Hello
	// Code is the link code the link presented, verified as signed by its key. It is a secret, and says nothing until the hub looks it up.
	Code    string
	session *yamux.Session
	control *Lines

	mu sync.Mutex // one writer on the control stream
}

// Handshake takes the control stream of a session and checks the link's key. Hub is the address links name for this hub. A link
// of another protocol version is told which side to upgrade, and the error says so too. The deadline of ctx is how long it gets: reads on
// a stream know no context.
func Handshake(ctx context.Context, session *yamux.Session, hub string) (*Peer, error) {
	stream, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	deadline, limited := ctx.Deadline()
	if !limited {
		deadline = time.Now().Add(30 * time.Second)
	}
	stream.SetDeadline(deadline)
	control := NewLines(stream)
	control.limit = maxHello
	var hello Hello
	if err := control.Read(&hello); err != nil {
		return nil, err
	}
	if hello.Protocol != Version {
		side := "the hub"
		if hello.Protocol < Version {
			side = "the project (a newer egzo, then `egzo project up`)"
		}
		refused := fmt.Sprintf("this hub speaks protocol %d and the link speaks %d: upgrade %s", Version, hello.Protocol, side)
		_ = control.Write(Greeting{Protocol: Version, Refused: refused})
		return nil, errors.New(refused)
	}
	if len(hello.Key) != ed25519.PublicKeySize {
		return nil, errors.New("the link's key is not an Ed25519 public key")
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	if err := control.Write(Greeting{Protocol: Version, Nonce: nonce}); err != nil {
		return nil, err
	}
	var proof Proof
	if err := control.Read(&proof); err != nil {
		return nil, err
	}
	if !ed25519.Verify(hello.Key, Signed(hub, nonce, hello.Key, proof.Code), proof.Signature) {
		return nil, errors.New("the link's signature does not verify")
	}
	stream.SetDeadline(time.Time{})
	control.limit = maxControl
	return &Peer{Hello: hello, Code: proof.Code, session: session, control: control}, nil
}

// Send tells the link where its registration stands.
func (p *Peer) Send(verdict Verdict) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.control.Write(verdict)
}

// Close ends the connection.
func (p *Peer) Close() error { return p.session.Close() }

// finishGrace is how long a link gets to hang up once it heard its last verdict.
var finishGrace = 2 * time.Second

// Finish ends a connection after its last verdict. The link hangs up when it hears one. Closing first would reset a connection with what the
// link pushed still on its way, and the verdict can go with it, so what it sends goes on being read until then. One that does not hang up
// is closed all the same.
func (p *Peer) Finish() error {
	select {
	case <-p.session.CloseChan():
	case <-time.After(finishGrace):
	}
	return p.session.Close()
}

// Closed is closed when the connection has ended, for whatever reason: what a verb or a terminal failed of can be told from what it
// was refused.
func (p *Peer) Closed() <-chan struct{} { return p.session.CloseChan() }

// Handlers is what a hub does with what a link pushes. They are called from the feed's own goroutine; what a link says is
// untrusted: names, status lines and audit events are data.
type Handlers struct {
	// Since says where the audit feed begins.
	Since   func() time.Time
	Project func(ProjectUpdate)
	Status  func(StatusLine)
	Audit   func(AuditEvent)
	// Unlink is the link asking to be forgotten; the verdict it returns goes back to the link, and is the last thing on the connection.
	Unlink func() Verdict
	// Pace is called with the bytes read from a feed, before they are looked at, and holds the feed back for as long as it should wait:
	// what is not read stays on the link's side, by yamux's flow control. nil paces nothing.
	Pace func(bytes int)
}

// maxFeeds is how many streams a link may hold open towards the hub: one per feed, and as many again for feeds that are being opened
// again. What a link does is untrusted, and each stream is a goroutine of the hub.
const maxFeeds = 6

// Run takes what the link sends until the connection ends.
func (p *Peer) Run(ctx context.Context, h Handlers) error {
	go p.readControl(h)
	slots := make(chan struct{}, maxFeeds)
	for {
		stream, err := p.session.AcceptStreamWithContext(ctx)
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			stream.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			defer stream.Close()
			if err := p.feed(stream, h); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, yamux.ErrStreamClosed) && !errors.Is(err, yamux.ErrSessionShutdown) {
				_ = stream.Close()
			}
		}()
	}
}

// controlPause is how long the hub waits between two messages of the control stream. A link has one thing to say on it, once.
var controlPause = 100 * time.Millisecond

// readControl reads the control stream, for as long as the connection is worth keeping: the stream ending, a line over its limit, and
// the answer to an Unlink all end the connection. The answer goes first: closing inside h.Unlink would lose it.
func (p *Peer) readControl(h Handlers) {
	defer p.Close()
	for {
		var message Unlink
		if err := p.control.Read(&message); err != nil {
			return
		}
		if message.Unlink && h.Unlink != nil {
			_ = p.Send(h.Unlink())
			_ = p.Finish()
			return
		}
		time.Sleep(controlPause)
	}
}

// paced reads a stream and holds its reader back: it counts bytes as they arrive, before anything decodes them.
type paced struct {
	net.Conn
	pace func(int)
}

func (r paced) Read(b []byte) (int, error) {
	n, err := r.Conn.Read(b)
	if n > 0 {
		r.pace(n)
	}
	return n, err
}

func (p *Peer) feed(stream net.Conn, h Handlers) error {
	if h.Pace != nil {
		stream = paced{Conn: stream, pace: h.Pace}
	}
	lines := NewLines(stream)
	var open FeedOpen
	if err := lines.Read(&open); err != nil {
		return err
	}
	start := FeedStart{}
	if open.Feed == FeedAudit && h.Since != nil {
		start.Since = h.Since()
	}
	if err := lines.Write(start); err != nil {
		return err
	}
	switch open.Feed {
	case FeedProject:
		return follow(lines, h.Project)
	case FeedStatus:
		return follow(lines, h.Status)
	case FeedAudit:
		return follow(lines, h.Audit)
	}
	return fmt.Errorf("no feed %q", open.Feed)
}

func follow[T any](lines *Lines, on func(T)) error {
	for {
		var item T
		if err := lines.Read(&item); err != nil {
			return err
		}
		if on != nil {
			on(item)
		}
	}
}

// call opens a stream for one verb and reads the replies up to the one that is done. The stream stays open: attach goes on on it.
func (p *Peer) call(ctx context.Context, request Request, progress func(string), result any) (*yamux.Stream, *Lines, func(), error) {
	stream, err := p.openStream(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	finished := make(chan struct{})
	var once sync.Once
	end := func() {
		once.Do(func() { close(finished) })
		stream.Close()
	}
	// Reads and writes on a stream know no context: a past deadline ends them.
	go func() {
		select {
		case <-ctx.Done():
			stream.SetDeadline(time.Now())
		case <-finished:
		}
	}()
	lines := NewLines(stream)
	if err := lines.Write(request); err != nil {
		end()
		return nil, nil, nil, err
	}
	for {
		var reply Reply
		if err := lines.Read(&reply); err != nil {
			end()
			if ctx.Err() != nil {
				return nil, nil, nil, ctx.Err()
			}
			return nil, nil, nil, err
		}
		if reply.Progress != "" && progress != nil {
			progress(reply.Progress)
		}
		if !reply.Done {
			continue
		}
		if reply.Gone {
			end()
			return nil, nil, nil, ErrNoProject{Name: p.Hello.Project}
		}
		if reply.Error != "" {
			end()
			return nil, nil, nil, errors.New(reply.Error)
		}
		if result != nil {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				end()
				return nil, nil, nil, err
			}
		}
		return stream, lines, end, nil
	}
}

func (p *Peer) ask(ctx context.Context, request Request, progress func(string), result any) error {
	_, _, end, err := p.call(ctx, request, progress, result)
	if err == nil {
		end()
	}
	return err
}

// Ask sends a verb by its name, without a result: for a test that a link refuses what is not one of its verbs.
func (p *Peer) Ask(ctx context.Context, verb string) error {
	return p.ask(ctx, Request{Verb: verb}, nil, nil)
}

// Project reads the project.
func (p *Peer) Project(ctx context.Context) (Project, error) {
	var project Project
	return project, p.ask(ctx, Request{Verb: VerbProject}, nil, &project)
}

// Published reads the project's templates.
func (p *Peer) Published(ctx context.Context) (Published, error) {
	var published Published
	return published, p.ask(ctx, Request{Verb: VerbPublished}, nil, &published)
}

// Spawn makes an instance of a template and returns its name.
func (p *Peer) Spawn(ctx context.Context, template, name, actor string, progress func(string)) (string, error) {
	var agent string
	return agent, p.ask(ctx, Request{Verb: VerbSpawn, Template: template, Name: name, Actor: actor}, progress, &agent)
}

// Start starts an instance.
func (p *Peer) Start(ctx context.Context, agent string) error {
	return p.ask(ctx, Request{Verb: VerbStart, Agent: agent}, nil, nil)
}

// Stop stops an instance.
func (p *Peer) Stop(ctx context.Context, agent string) error {
	return p.ask(ctx, Request{Verb: VerbStop, Agent: agent}, nil, nil)
}

// Remove removes an instance and what it owns.
func (p *Peer) Remove(ctx context.Context, agent string, progress func(string)) error {
	return p.ask(ctx, Request{Verb: VerbRemove, Agent: agent}, progress, nil)
}

// Events returns the latest events of the project's control sidecar, oldest first.
func (p *Peer) Events(ctx context.Context, agent string, limit int) ([]Event, error) {
	var events []Event
	return events, p.ask(ctx, Request{Verb: VerbEvents, Agent: agent, Limit: limit}, nil, &events)
}

// Logs returns the end of a container's log.
func (p *Peer) Logs(ctx context.Context, container string, tail int) (string, error) {
	var logs string
	return logs, p.ask(ctx, Request{Verb: VerbLogs, Container: container, Tail: tail}, nil, &logs)
}

// Spec returns the project's published templates, without any secret value.
func (p *Peer) Spec(ctx context.Context) ([]byte, error) {
	var spec []byte
	return spec, p.ask(ctx, Request{Verb: VerbSpec}, nil, &spec)
}

// DialPort connects to a port an agent exposed. As with a net.Dialer, ctx covers the dial and nothing after it: the connection lasts until it
// is closed or the link goes.
func (p *Peer) DialPort(ctx context.Context, agent string, port int) (net.Conn, error) {
	stream, err := p.openStream(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { stream.SetDeadline(time.Now()) }) // reads and writes on a stream know no context
	lines := NewLines(stream)
	failed := func(err error) (net.Conn, error) {
		stop()
		stream.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := lines.Write(Request{Verb: VerbForward, Agent: agent, Port: port}); err != nil {
		return failed(err)
	}
	for {
		var reply Reply
		if err := lines.Read(&reply); err != nil {
			return failed(err)
		}
		if !reply.Done {
			continue
		}
		if reply.Gone {
			return failed(ErrNoProject{Name: p.Hello.Project})
		}
		if reply.Error != "" {
			return failed(errors.New(reply.Error))
		}
		if !stop() {
			return failed(context.Canceled)
		}
		return &portConn{Stream: stream, reader: lines.Rest()}, nil
	}
}

// openStream opens a stream within ctx. OpenStream knows no context, and waits while the far side leaves too many streams unanswered: past
// ctx it goes on waiting on its own, until the session ends, and closes what it gets.
func (p *Peer) openStream(ctx context.Context) (*yamux.Stream, error) {
	type opened struct {
		stream *yamux.Stream
		err    error
	}
	done := make(chan opened, 1)
	go func() {
		stream, err := p.session.OpenStream()
		done <- opened{stream, err}
	}()
	select {
	case o := <-done:
		return o.stream, o.err
	case <-ctx.Done():
		go func() {
			if o := <-done; o.err == nil {
				o.stream.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// portConn is the stream of a forward: what was read past the reply comes first.
type portConn struct {
	*yamux.Stream
	reader io.Reader
}

func (c *portConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

// CloseWrite ends what this side sends, and leaves the way back open: a stream's Close is a half close.
func (c *portConn) CloseWrite() error { return c.Stream.Close() }

// Close ends both ways here. The far side hears the end of its input, and goes by what the exposure's protocol says about that.
func (c *portConn) Close() error {
	err := c.Stream.Close()
	c.Stream.SetDeadline(time.Now())
	return err
}

// Attach connects a terminal to an agent's session until it ends or ctx is cancelled.
func (p *Peer) Attach(ctx context.Context, agent string, term Terminal) error {
	stream, lines, end, err := p.call(ctx, Request{Verb: VerbAttach, Agent: agent, Size: term.Size}, nil, nil)
	if err != nil {
		return err
	}
	defer end()
	var mu sync.Mutex // input and resizes share the stream
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buffer := make([]byte, maxFrame)
		for {
			n, err := term.In.Read(buffer)
			if n > 0 {
				mu.Lock()
				werr := WriteInput(stream, buffer[:n])
				mu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				stream.Close() // the end of input is the end of the stream's writing side
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case size, ok := <-term.Resize:
				if !ok {
					return
				}
				mu.Lock()
				err := WriteResize(stream, size)
				mu.Unlock()
				if err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	_, err = io.Copy(term.Out, lines.Rest())
	if ctx.Err() != nil {
		return nil
	}
	return err
}
