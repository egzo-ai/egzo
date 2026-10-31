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
	Hello   Hello
	session *yamux.Session
	control *Lines

	mu sync.Mutex // one writer on the control stream
}

// Handshake takes the control stream of a session and checks the link's key. Hub is the address links name for this hub. A link
// of another protocol version is told which side to upgrade, and the error says so too.
func Handshake(ctx context.Context, session *yamux.Session, hub string) (*Peer, error) {
	stream, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	stream.SetDeadline(time.Now().Add(30 * time.Second))
	control := NewLines(stream)
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
	if !ed25519.Verify(hello.Key, Signed(hub, nonce, hello.Key), proof.Signature) {
		return nil, errors.New("the link's signature does not verify")
	}
	stream.SetDeadline(time.Time{})
	return &Peer{Hello: hello, session: session, control: control}, nil
}

// Send tells the link where its registration stands.
func (p *Peer) Send(verdict Verdict) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.control.Write(verdict)
}

// Close ends the connection.
func (p *Peer) Close() error { return p.session.Close() }

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
	// Unlink is the link asking to be forgotten; the verdict it returns goes back to the link.
	Unlink func() Verdict
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

func (p *Peer) readControl(h Handlers) {
	for {
		var message Unlink
		if err := p.control.Read(&message); err != nil {
			return
		}
		if message.Unlink && h.Unlink != nil {
			_ = p.Send(h.Unlink())
		}
	}
}

func (p *Peer) feed(stream net.Conn, h Handlers) error {
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
	stream, err := p.session.OpenStream()
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
