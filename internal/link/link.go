// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package link is the link sidecar: the project's connection to its hub. It dials out, proves its key, and once a person has
// approved the project on the hub it does what the hub asks, with egzo's verbs and nothing else, on its own engine, and pushes the
// project, the agents' status and the proxy's audit trail. Nothing here listens on the network.
package link

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/seal"
	"github.com/egzo-ai/egzo/internal/version"
)

// Status is what the link knows of itself.
type Status = hublink.Status

// Options are the link's settings.
type Options struct {
	Project string
	Hub     string
	CA      []byte
	Dir     string // the link's volume
	Engine  *Engine
}

const (
	keyFile    = "link.key"
	firstDelay = time.Second
	lastDelay  = time.Minute
	dialLimit  = 30 * time.Second
)

// Link is one project's link.
type Link struct {
	options Options

	mu      sync.Mutex
	status  Status
	changed chan struct{} // closed and replaced when the status changes
	key     ed25519.PrivateKey
	proxy   string
	control *hublink.Lines // the live control stream, for the last word
	verdict chan hublink.Verdict
	unlock  chan struct{} // closed once the key is open
	gone    chan struct{} // closed when the hub has forgotten the project
}

// New makes a link, locked until Unlock.
func New(options Options) *Link {
	return &Link{
		options: options, status: Status{Hub: options.Hub, State: hublink.StateLocked},
		changed: make(chan struct{}), unlock: make(chan struct{}), gone: make(chan struct{}),
	}
}

// NoEngine records that the engine's socket cannot be reached: the link stays up to say so, and `egzo ps` shows it.
func (l *Link) NoEngine(err error) {
	l.setState(hublink.StateNoEngine, err.Error())
}

// Status returns where the link stands.
func (l *Link) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Settled waits until the link has an answer, or for wait, and returns where it stands.
func (l *Link) Settled(ctx context.Context, wait time.Duration) Status {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		l.mu.Lock()
		status, changed := l.status, l.changed
		l.mu.Unlock()
		if status.State != hublink.StateConnecting && status.State != hublink.StateLocked {
			return status
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return status
		}
	}
}

func (l *Link) set(change func(*Status)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	change(&l.status)
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *Link) setState(state, failure string) {
	l.set(func(s *Status) {
		*s = Status{Hub: s.Hub, State: state, Project: s.Project, Owner: s.Owner, Error: failure}
	})
}

// Unlock opens the link's key with the project's secret key, or makes the key on the first run. The link's identity is what the
// hub knows the project by, so a volume sealed with another secret key is refused and nothing changes.
func (l *Link) Unlock(secret []byte, proxy string) error {
	if l.options.Engine == nil {
		return nil // nothing to connect for: the status says why
	}
	keys, err := seal.New(secret, l.options.Project)
	if err != nil {
		return err
	}
	key, err := loadKey(l.options.Dir, keys)
	if err != nil {
		l.mu.Lock()
		locked := l.key == nil
		l.mu.Unlock()
		if locked { // a link that holds its key goes on serving: only `up` was given the wrong one
			l.setState(hublink.StateLocked, err.Error())
		}
		return err
	}
	l.mu.Lock()
	first := l.key == nil
	l.key, l.proxy = key, proxy
	l.mu.Unlock()
	if first {
		l.setState(hublink.StateConnecting, "")
		close(l.unlock)
	}
	return nil
}

// Rekey seals the link's key with another secret key: how the link follows `egzo project rotate-ca`. Only a link that holds its key
// can: one that restarted has only the sealed file, and needs the old secret key first.
func (l *Link) Rekey(secret []byte) error {
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	if key == nil {
		return errors.New("the link is locked: run `egzo project up` with the old secret key, then rotate again")
	}
	keys, err := seal.New(secret, l.options.Project)
	if err != nil {
		return err
	}
	return storeKey(l.options.Dir, keys, key)
}

// storeKey replaces the sealed key in one step: a link that dies half way keeps the one it had.
func storeKey(dir string, keys *seal.Keys, key ed25519.PrivateKey) error {
	sealed, err := keys.Seal(seal.UseLink, key.Seed())
	if err != nil {
		return err
	}
	path := filepath.Join(dir, keyFile)
	if err := os.WriteFile(path+".new", sealed, 0o600); err != nil {
		return fmt.Errorf("keep the link's key: %w", err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		return fmt.Errorf("keep the link's key: %w", err)
	}
	return nil
}

func loadKey(dir string, keys *seal.Keys) (ed25519.PrivateKey, error) {
	path := filepath.Join(dir, keyFile)
	sealed, err := os.ReadFile(path)
	if err == nil {
		seed, err := keys.Open(seal.UseLink, sealed)
		if errors.Is(err, seal.ErrWrongKey) {
			return nil, errors.New("the link's key was sealed with another secret key: `egzo project rotate-ca` moves a running link to this one; " +
				"otherwise give that key back, or `egzo project down` and register again")
		}
		if err != nil {
			return nil, err
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := storeKey(dir, keys, private); err != nil {
		return nil, err
	}
	return private, nil
}

// Run keeps the link connected until ctx ends or the hub has forgotten the project.
func (l *Link) Run(ctx context.Context) error {
	select {
	case <-l.unlock:
	case <-ctx.Done():
		return nil
	}
	delay := firstDelay
	for {
		began := time.Now()
		err := l.connect(ctx)
		select {
		case <-l.gone:
			return nil
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "the connection to %s: %v\n", l.options.Hub, err)
			l.setState(hublink.StateUnreachable, err.Error())
		}
		if time.Since(began) > lastDelay {
			delay = firstDelay // it was connected: this is a new failure
		}
		select {
		case <-time.After(delay + mathrand.N(delay/4)):
		case <-ctx.Done():
			return nil
		}
		delay = min(delay*2, lastDelay)
	}
}

// connect is one connection, from the dial to its end.
func (l *Link) connect(ctx context.Context) error {
	l.mu.Lock()
	key, proxy := l.key, l.proxy
	l.mu.Unlock()
	dialing, cancel := context.WithTimeout(ctx, dialLimit)
	defer cancel()
	session, err := hublink.Dialer{Hub: l.options.Hub, CA: l.options.CA, Proxy: proxy}.Dial(dialing)
	if err != nil {
		return err
	}
	defer session.Close()
	stream, err := session.OpenStream()
	if err != nil {
		return err
	}
	control := hublink.NewLines(stream)
	if err := control.Write(l.hello(ctx, key)); err != nil {
		return err
	}
	var greeting hublink.Greeting
	if err := control.Read(&greeting); err != nil {
		return fmt.Errorf("the hub did not answer: %w", err)
	}
	if greeting.Refused != "" {
		return errors.New(greeting.Refused)
	}
	public := key.Public().(ed25519.PublicKey)
	if err := control.Write(hublink.Proof{Signature: ed25519.Sign(key, hublink.Signed(l.options.Hub, greeting.Nonce, public))}); err != nil {
		return err
	}
	l.mu.Lock()
	l.control = control
	l.verdict = make(chan hublink.Verdict, 1)
	verdicts := l.verdict
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.control = nil
		l.mu.Unlock()
	}()

	// Nothing the hub asks runs and nothing is pushed unless the project is approved, for as long as it is.
	var approval atomic.Pointer[context.Context]
	go l.serve(ctx, session, &approval)
	var stopServing context.CancelFunc
	startServing := func() {
		serving, cancel := context.WithCancel(ctx)
		stopServing = cancel
		approval.Store(&serving)
		go l.pushFeeds(serving, session)
	}
	endServing := func() {
		if stopServing != nil {
			approval.Store(nil)
			stopServing()
			stopServing = nil
		}
	}
	defer endServing()
	for {
		var verdict hublink.Verdict
		if err := control.Read(&verdict); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("the connection to the hub ended: %w", err)
		}
		select {
		case <-verdicts:
		default:
		}
		verdicts <- verdict
		l.record(verdict)
		switch {
		case verdict.State == hublink.StateUnlinked:
			close(l.gone)
			return nil
		case verdict.State != hublink.StateApproved:
			endServing()
		case stopServing == nil:
			startServing()
		}
	}
}

func (l *Link) hello(ctx context.Context, key ed25519.PrivateKey) hublink.Hello {
	hello := hublink.Hello{Protocol: hublink.Version, Project: l.options.Project, Release: version.Version, Key: key.Public().(ed25519.PublicKey)}
	if info, err := l.options.Engine.Client().API.Info(ctx); err == nil {
		hello.Host = info.Name
	}
	if published, err := l.options.Engine.Published(ctx, l.options.Project); err == nil {
		hello.Templates = published.Names()
	}
	return hello
}

func (l *Link) record(v hublink.Verdict) {
	l.set(func(s *Status) {
		*s = Status{Hub: s.Hub, State: v.State, Project: v.Project, Owner: v.Owner, Code: v.Code, Address: v.Address, Error: v.Message}
	})
}

// unlinkWait is how long Unlink waits for a connection: long enough for a link between two connections to come back.
var unlinkWait = 5 * time.Second

// Unlink tells the hub to forget the project and waits for its answer. A connection can end at any moment and come back by itself,
// so one that is down is waited for. A project the hub cannot be told about stays on the hub until its operator removes it.
func (l *Link) Unlink(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, unlinkWait)
	defer cancel()
	for {
		l.mu.Lock()
		control, verdicts, changed := l.control, l.verdict, l.changed
		l.mu.Unlock()
		if control != nil && control.Write(hublink.Unlink{Unlink: true}) == nil {
			return l.awaitUnlinked(verdicts)
		}
		select {
		case <-l.gone:
			return nil
		case <-changed:
		case <-ctx.Done():
			return errors.New("the hub is not connected: it still lists this project, and its operator can revoke it")
		}
	}
}

func (l *Link) awaitUnlinked(verdicts <-chan hublink.Verdict) error {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case verdict := <-verdicts:
			if verdict.State == hublink.StateUnlinked {
				return nil
			}
		case <-l.gone:
			return nil
		case <-timeout:
			return errors.New("the hub did not answer")
		}
	}
}

// ignoreFor is how long a stream that came without an approval is held before it is closed.
var ignoreFor = time.Minute

// serve takes the streams the hub opens for as long as the session lasts, and answers those that come while the project is approved.
// Streams must always be taken: yamux would queue the others, run them when an approval comes, long after their caller gave up, and
// the hub would close a healthy session over a stream left unaccepted.
func (l *Link) serve(ctx context.Context, session *yamux.Session, approval *atomic.Pointer[context.Context]) {
	for {
		stream, err := session.AcceptStreamWithContext(ctx)
		if err != nil {
			return
		}
		approved := approval.Load()
		if approved == nil {
			go ignore(stream)
			continue
		}
		serving := *approved
		go func() {
			defer stream.Close()
			finished := make(chan struct{})
			defer close(finished)
			go func() { // a verb running when the approval ends is cut
				select {
				case <-serving.Done():
					stream.SetDeadline(time.Now())
				case <-finished:
				}
			}()
			l.answer(serving, stream)
		}()
	}
}

// ignore says nothing to a stream and drops what comes on it: a project that is not approved does not answer, not even to refuse.
func ignore(stream net.Conn) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(ignoreFor))
	_, _ = io.Copy(io.Discard, stream)
}

// answer runs one verb.
func (l *Link) answer(ctx context.Context, stream net.Conn) {
	lines := hublink.NewLines(stream)
	var request hublink.Request
	if err := lines.Read(&request); err != nil || ctx.Err() != nil {
		return
	}
	engine, project := l.options.Engine, l.options.Project
	progress := func(step string) { _ = lines.Write(hublink.Reply{Progress: step}) }
	var result any
	var err error
	switch request.Verb {
	case hublink.VerbProject:
		result, err = engine.Project(ctx, project)
	case hublink.VerbPublished:
		result, err = engine.Published(ctx, project)
	case hublink.VerbSpawn:
		result, err = engine.Spawn(ctx, project, request.Template, request.Name, request.Actor, progress)
	case hublink.VerbStart:
		err = engine.Start(ctx, project, request.Agent)
	case hublink.VerbStop:
		err = engine.Stop(ctx, project, request.Agent)
	case hublink.VerbRemove:
		err = engine.Remove(ctx, project, request.Agent, progress)
	case hublink.VerbEvents:
		result, err = engine.Events(ctx, project, request.Agent, request.Limit)
	case hublink.VerbLogs:
		result, err = engine.Logs(ctx, project, request.Container, request.Tail)
	case hublink.VerbSpec:
		result, err = engine.Spec(ctx, project)
	case hublink.VerbAttach:
		if err = engine.Attachable(ctx, project, request.Agent); err == nil {
			_ = lines.Write(hublink.Reply{Done: true})
			l.attach(ctx, stream, lines, request)
			return
		}
	default:
		err = fmt.Errorf("no verb %q", request.Verb)
	}
	reply := hublink.Reply{Done: true}
	var gone ErrNoProject
	switch {
	case errors.As(err, &gone):
		reply.Gone = true
	case err != nil:
		reply.Error = err.Error()
	case result != nil:
		reply.Result, err = json.Marshal(result)
		if err != nil {
			reply.Error = err.Error()
		}
	}
	_ = lines.Write(reply)
}

// attach is a terminal on the verb's stream: raw output, and input frames from the hub.
func (l *Link) attach(ctx context.Context, stream net.Conn, lines *hublink.Lines, request hublink.Request) {
	input, writer := io.Pipe()
	resize := make(chan hublink.Size, 8)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		err := hublink.ReadFrames(lines.Rest(), writer, func(size hublink.Size) {
			select {
			case resize <- size:
			default:
			}
		})
		writer.CloseWithError(err)
		cancel() // the hub's side is gone: the terminal goes with it
	}()
	if err := l.options.Engine.Attach(ctx, l.options.Project, request.Agent, Terminal{In: input, Out: stream, Size: request.Size, Resize: resize}); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "attach %s: %s\n", request.Agent, strings.TrimSpace(err.Error()))
	}
}
