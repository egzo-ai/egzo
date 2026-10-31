// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
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

// Closed is closed when the connection has ended, for whatever reason: what a verb or a terminal failed of can be told from what it
// was refused.
func (p *Peer) Closed() <-chan struct{} { return p.session.CloseChan() }
