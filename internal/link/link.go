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
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	unlock  chan struct{} // closed once the key is open
	wake    chan struct{} // a link that stopped dialing, or waits to dial again, goes on: holds one
}

// New makes a link, locked until Unlock.
func New(options Options) *Link {
	return &Link{
		options: options, status: Status{Hub: options.Hub, State: hublink.StateLocked},
		changed: make(chan struct{}), unlock: make(chan struct{}), wake: make(chan struct{}, 1),
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

// attempt is one connection, and says how it ended.
func (l *Link) attempt(ctx context.Context) error {
	err := l.connect(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errTurnedAway) {
		fmt.Fprintf(os.Stderr, "the connection to %s: %v\n", l.options.Hub, err)
		l.setState(hublink.StateUnreachable, err.Error())
	}
	return err
}

// connect is one connection, from the dial to its end.
func (l *Link) connect(ctx context.Context) error {
	l.mu.Lock()
	key, proxy := l.key, l.proxy
	l.mu.Unlock()
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
	if err := control.Write(hublink.Proof{Signature: ed25519.Sign(key, hublink.Signed(l.options.Hub, greeting.Nonce, public, ""))}); err != nil {
		return err
	}
	for {
		var verdict hublink.Verdict
		if err := control.Read(&verdict); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("the connection to the hub ended: %w", err)
		}
		l.record(verdict)
		if verdict.State != hublink.StateApproved {
			return errTurnedAway
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
		*s = Status{Hub: s.Hub, State: v.State, Project: v.Project, Owner: v.Owner, Error: v.Message}
	})
}
