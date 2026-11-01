// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package link is the link sidecar: the project's connection to its hub. It dials out and proves its key, and once a link code has
// registered the project on the hub it does what the hub asks, with egzo's verbs and nothing else, on its own engine, and pushes the
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
	"github.com/egzo-ai/egzo/internal/portforward"
	"github.com/egzo-ai/egzo/internal/seal"
	"github.com/egzo-ai/egzo/internal/termsafe"
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

// errTurnedAway ends a connection the hub said no to, by a verdict: it has closed it, and dialing again changes nothing.
var errTurnedAway = errors.New("the hub turned the link away")

// Link is one project's link.
type Link struct {
	options Options

	mu      sync.Mutex
	status  Status
	changed chan struct{} // closed and replaced when the status changes
	key     ed25519.PrivateKey
	proxy   string
	control *hublink.Lines     // the live control stream, for the last word
	asked   *registering       // the code `egzo project register` brought, until the hub has answered it
	unlock  chan struct{}      // closed once the key is open
	handing context.CancelFunc // ends the handing on of an older template
	wake    chan struct{}      // a link that stopped dialing, or waits to dial again, goes on: holds one
	gone    chan struct{}      // closed when the hub has forgotten the project

	template sync.Mutex // one template at a time goes to the control sidecar

	exposed map[string]map[int]string // the protocol of what each agent exposes, as the latest status line says
}

// registering is one `egzo project register`. The code stays in memory, and only until the hub answers: it is never in a Status, a log
// line or an error.
type registering struct {
	code string
	done chan struct{} // closed when it has an answer
}

// New makes a link, locked until Unlock.
func New(options Options) *Link {
	return &Link{
		options: options, status: Status{Hub: options.Hub, State: hublink.StateLocked},
		changed: make(chan struct{}), unlock: make(chan struct{}), wake: make(chan struct{}, 1), gone: make(chan struct{}),
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
		return nil
	}
	l.again()
	return nil
}

// again makes a link that stopped dialing try once more: `up`, or a new code, may have changed what the hub says. What it said last no
// longer stands, so the status says so until the hub answers.
func (l *Link) again() {
	l.set(func(s *Status) {
		if s.State == hublink.StateUnregistered || s.State == hublink.StateRevoked {
			*s = Status{Hub: s.Hub, State: hublink.StateConnecting}
		}
	})
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// registerWait is how long Register waits for the hub: a dial, a handshake, and its answer.
var registerWait = dialLimit + 5*time.Second

// Register presents a link code to the hub and returns what it answered. A project that is registered already spends no code.
func (l *Link) Register(ctx context.Context, code string) (Status, error) {
	l.mu.Lock()
	status := l.status
	switch {
	case status.State == hublink.StateApproved:
		l.mu.Unlock()
		return status, nil
	case status.State == hublink.StateNoEngine, status.State == hublink.StateLocked:
		l.mu.Unlock()
		return status, errors.New(status.Text())
	case l.asked != nil:
		l.mu.Unlock()
		return status, errors.New("another `egzo project register` is running")
	}
	asked := &registering{code: code, done: make(chan struct{})}
	l.asked = asked
	l.mu.Unlock()
	defer l.answered(asked)
	l.again()
	ctx, cancel := context.WithTimeout(ctx, registerWait)
	defer cancel()
	select {
	case <-asked.done:
		return l.Status(), nil
	case <-ctx.Done():
		return l.Status(), errors.New("the hub did not answer")
	}
}

// answered ends a registration: the code is dropped here.
func (l *Link) answered(asked *registering) {
	if asked == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.asked == asked {
		l.asked = nil
		close(asked.done)
	}
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
		err := l.attempt(ctx)
		select {
		case <-l.gone:
			return nil
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errTurnedAway) {
			// Nothing changes by dialing again: a person has to. A hub is not asked once a minute by what it has refused.
			delay = firstDelay
			select {
			case <-l.wake:
			case <-ctx.Done():
				return nil
			}
			continue
		}
		if time.Since(began) > lastDelay {
			delay = firstDelay // it was connected: this is a new failure
		}
		select {
		case <-time.After(delay + mathrand.N(delay/4)):
			delay = min(delay*2, lastDelay)
		case <-l.wake:
			delay = firstDelay
		case <-ctx.Done():
			return nil
		}
	}
}

func (l *Link) current() *registering {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

// attempt is one connection, with the code `egzo project register` brought if there is one, and says how it ended. A code that did not get
// an answer is not tried again: the person who gave it hears that the hub cannot be reached. If it was spent all the same, the key is
// approved the next time the link dials.
func (l *Link) attempt(ctx context.Context) error {
	asked := l.current()
	err := l.connect(ctx, asked)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errTurnedAway) {
		fmt.Fprintf(os.Stderr, "the connection to %s: %v\n", l.options.Hub, err)
		l.setState(hublink.StateUnreachable, err.Error())
		l.answered(asked)
	}
	return err
}

// connect is one connection, from the dial to its end.
func (l *Link) connect(ctx context.Context, asked *registering) error {
	l.mu.Lock()
	key, proxy := l.key, l.proxy
	l.mu.Unlock()
	code := ""
	if asked != nil {
		code = asked.code
	}
	// Before the dial: the hub gives a handshake little time, and what an engine answers takes what it takes.
	hello := l.hello(ctx, key)
	dialing, cancel := context.WithTimeout(ctx, dialLimit)
	defer cancel()
	session, err := hublink.Dialer{Hub: l.options.Hub, CA: l.options.CA, Proxy: proxy}.Dial(dialing)
	if err != nil {
		return err
	}
	defer session.Close()
	defer context.AfterFunc(ctx, func() { session.Close() })() // what follows reads a stream, which knows no context
	stream, err := session.OpenStream()
	if err != nil {
		return err
	}
	control := hublink.NewLines(stream)
	if err := control.Write(hello); err != nil {
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
	if err := control.Write(hublink.Proof{Signature: ed25519.Sign(key, hublink.Signed(l.options.Hub, greeting.Nonce, public, code)), Code: code}); err != nil {
		return err
	}
	l.mu.Lock()
	l.control = control
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.control = nil
		l.mu.Unlock()
	}()

	// Nothing the hub asks runs and nothing is pushed unless the project is approved, for as long as it is. Whatever else the hub says is
	// the last thing on this connection.
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
		l.record(verdict)
		l.handTemplate(ctx, verdict.Preview)
		// An answer to a connection that came without the code says nothing of the code, unless it is one the code would change nothing for.
		if now := l.current(); now == asked || verdict.State != hublink.StateUnregistered {
			l.answered(now)
		}
		switch {
		case verdict.State == hublink.StateUnlinked:
			close(l.gone)
			return nil
		case verdict.State != hublink.StateApproved:
			return errTurnedAway
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

// handTemplate tells the control sidecar what the hub says a preview's address is, or that it serves none, for as long as it takes: a sidecar
// being made again is an ordinary moment, and a verdict is not held up for it. A newer verdict replaces what is being handed on.
func (l *Link) handTemplate(ctx context.Context, template string) {
	template = usableTemplate(template)
	ctx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	if l.handing != nil {
		l.handing()
	}
	l.handing = cancel
	l.mu.Unlock()
	go func() {
		l.template.Lock()
		defer l.template.Unlock()
		for delay := firstDelay; ctx.Err() == nil; delay = min(delay*2, lastDelay) {
			err := l.options.Engine.SetPreviewTemplate(ctx, l.options.Project, template)
			if err == nil {
				return
			}
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "hand the preview address to the control sidecar: %v\n", err)
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
			}
		}
	}()
}

// usableTemplate is what to hand on of what the hub said. A template the control sidecar would refuse is no template: trying again would
// never end, and an older one would go on being given out.
func usableTemplate(template string) string {
	if template != "" && !hublink.ValidPreviewTemplate(template) {
		fmt.Fprintf(os.Stderr, "the hub's preview address is not an address with one %s in its host name: %q\n", hublink.PreviewPlaceholder, template)
		return ""
	}
	return template
}

func (l *Link) record(v hublink.Verdict) {
	l.set(func(s *Status) {
		*s = Status{Hub: s.Hub, State: v.State, Project: v.Project, Owner: v.Owner, Error: v.Message}
	})
}

// unlinkWait is how long Unlink waits for a connection: long enough for a link between two connections to come back.
var unlinkWait = 5 * time.Second

// Unlink tells the hub to forget the project and waits for its answer, which is the state the link ends in: unlinked, or unregistered or
// revoked for a project the hub had nothing to forget of, and that has no connection to ask on. A connection can end at any moment and come
// back by itself, so one that is down is waited for. A project the hub cannot be told about stays on the hub until its operator removes it.
func (l *Link) Unlink(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, unlinkWait)
	defer cancel()
	for {
		l.mu.Lock()
		state, control, changed := l.status.State, l.control, l.changed
		l.mu.Unlock()
		switch {
		case state == hublink.StateUnlinked, state == hublink.StateUnregistered, state == hublink.StateRevoked:
			return state, nil
		case control != nil && control.Write(hublink.Unlink{Unlink: true}) == nil:
			return l.awaitUnlinked()
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return "", errors.New("the hub is not connected: it still lists this project, and its operator can revoke it")
		}
	}
}

// unlinkAnswer is how long the hub gets to answer an Unlink.
var unlinkAnswer = 10 * time.Second

// awaitUnlinked waits for the end of it. The hub closes the connection as it answers, so an answer can be lost: the link dials again and
// hears it is not registered, which is the same answer.
func (l *Link) awaitUnlinked() (string, error) {
	timeout := time.After(unlinkAnswer)
	for {
		l.mu.Lock()
		state, changed := l.status.State, l.changed
		l.mu.Unlock()
		switch state {
		case hublink.StateUnregistered:
			return hublink.StateUnlinked, nil
		case hublink.StateUnlinked, hublink.StateRevoked:
			return state, nil
		}
		select {
		case <-changed:
		case <-timeout:
			return "", errors.New("the hub did not answer")
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

// maxResult is the longest result a reply carries once it is JSON: what the hub reads is a line of at most hublink.MaxLine. Over that
// it would refuse the stream, and say nothing of why.
const maxResult = hublink.MaxLine - 1<<16

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
	case hublink.VerbForward:
		protocol, ok := l.exposure(request.Agent, request.Port)
		if !ok {
			err = fmt.Errorf("agent %q does not expose port %d", request.Agent, request.Port)
			break
		}
		l.forward(ctx, stream, lines, request, protocol)
		return
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
		reply.Result, reply.Error = encodeResult(request.Verb, result)
	}
	_ = lines.Write(reply)
}

// encodeResult is a verb's result as a reply carries it, or why it cannot.
func encodeResult(verb string, result any) (json.RawMessage, string) {
	encoded, err := json.Marshal(result)
	switch {
	case err != nil:
		return nil, err.Error()
	case len(encoded) > maxResult:
		return nil, fmt.Sprintf("the answer to %s is %d bytes, over the %d a reply can carry", verb, len(encoded), maxResult)
	}
	return encoded, ""
}

// noteExposures keeps what the status feed says is exposed.
func (l *Link) noteExposures(line StatusLine) {
	exposed := make(map[string]map[int]string, len(line.Agents))
	for _, a := range line.Agents {
		for _, e := range a.Exposed {
			if exposed[a.Agent] == nil {
				exposed[a.Agent] = map[int]string{}
			}
			exposed[a.Agent][e.Port] = e.Protocol
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exposed = exposed
}

// forgetExposures is for when the status feed stopped: what it said last may no longer be true.
func (l *Link) forgetExposures() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exposed = nil
}

func (l *Link) exposure(agent string, port int) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	protocol, ok := l.exposed[agent][port]
	return protocol, ok
}

// forward is one connection to a port on the verb's stream. The reply waits for the command to say it reached the server, so that a port where
// nothing listens is an answer: a server says nothing until it is spoken to, so a stream that ended could mean anything. The two ways out of the
// command need not arrive in order, so a byte of the server's says the same. The protocol says what the end of the hub's side means: an HTTP
// proxy never half closes, so it has left, and a tcp client may still be waiting for its answer.
func (l *Link) forward(ctx context.Context, stream net.Conn, lines *hublink.Lines, request hublink.Request, protocol string) {
	var answered sync.Once
	connected := func() { answered.Do(func() { _ = lines.Write(hublink.Reply{Done: true}) }) }
	said := &forwardWords{connected: connected}
	out := firstByte{Writer: stream, connected: connected}
	err := l.options.Engine.Forward(ctx, l.options.Project, request.Agent, request.Port, protocol == hublink.ProtocolHTTP, lines.Rest(), out, said)
	answered.Do(func() {
		reason := said.reason()
		if reason == "" && err != nil {
			reason = err.Error()
		}
		if reason == "" {
			reason = "the forward ended before it reached the server"
		}
		_ = lines.Write(hublink.Reply{Done: true, Error: reason})
	})
}

// firstByte writes what the server said, which proves it was reached.
type firstByte struct {
	io.Writer
	connected func()
}

func (w firstByte) Write(data []byte) (int, error) {
	w.connected()
	return w.Writer.Write(data)
}

// forwardWords reads what the command says on stderr: that it connected, or the start of why it failed.
type forwardWords struct {
	connected func()
	words     []byte
}

func (w *forwardWords) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), portforward.Connected) {
		w.connected()
		return len(data), nil
	}
	if room := 1024 - len(w.words); room > 0 {
		w.words = append(w.words, data[:min(room, len(data))]...)
	}
	return len(data), nil
}

func (w *forwardWords) reason() string {
	return termsafe.Truncate(termsafe.Line(strings.TrimSpace(string(w.words))), 200)
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
