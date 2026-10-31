// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

const address = "https://hub.example.org"

// pair is a session over a pipe, the link's side first.
func pair(t *testing.T) (*yamux.Session, *yamux.Session) {
	t.Helper()
	a, b := net.Pipe()
	link, err := yamux.Client(a, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	hub, err := yamux.Server(b, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { link.Close(); hub.Close() })
	return link, hub
}

type handshake struct {
	protocol int
	hub      string // the address the link signs
	key      ed25519.PrivateKey
	code     string             // the code it presents
	signed   *string            // the code it signs, when it is not the one it presents
	signer   ed25519.PrivateKey // who signs, when it is not the key it says it is
}

// run plays the link's side of a handshake against the hub's, and returns the hub's result and the greeting the link got.
func (h handshake) run(t *testing.T) (*Peer, Greeting, error) {
	t.Helper()
	link, hub := pair(t)
	done := make(chan Greeting, 1)
	go func() {
		stream, err := link.OpenStream()
		if err != nil {
			t.Error(err)
			return
		}
		lines := NewLines(stream)
		public := h.key.Public().(ed25519.PublicKey)
		_ = lines.Write(Hello{Protocol: h.protocol, Project: "web", Key: public})
		var greeting Greeting
		_ = lines.Read(&greeting)
		if greeting.Refused == "" {
			signed, signer := h.code, h.key
			if h.signed != nil {
				signed = *h.signed
			}
			if h.signer != nil {
				signer = h.signer
			}
			_ = lines.Write(Proof{Signature: ed25519.Sign(signer, Signed(h.hub, greeting.Nonce, public, signed)), Code: h.code})
		}
		done <- greeting
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, err := Handshake(ctx, hub, address)
	return peer, <-done, err
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAKeyThatSignsTheChallengeIsLetIn(t *testing.T) {
	peer, greeting, err := handshake{protocol: Version, hub: address, key: newKey(t)}.run(t)
	if err != nil || peer == nil || peer.Hello.Project != "web" || len(greeting.Nonce) != 32 || peer.Code != "" {
		t.Fatalf("peer %+v, greeting %+v, err %v", peer, greeting, err)
	}
}

func TestACodeComesWithTheProofAndIsCoveredByIt(t *testing.T) {
	peer, _, err := handshake{protocol: Version, hub: address, key: newKey(t), code: "egzo-link-abc"}.run(t)
	if err != nil || peer.Code != "egzo-link-abc" {
		t.Fatalf("peer %+v, err %v", peer, err)
	}
	other := "egzo-link-another"
	if _, _, err := (handshake{protocol: Version, hub: address, key: newKey(t), code: "egzo-link-abc", signed: &other}).run(t); err == nil {
		t.Error("a code that was not signed was taken")
	}
	nothing := ""
	if _, _, err := (handshake{protocol: Version, hub: address, key: newKey(t), code: "egzo-link-abc", signed: &nothing}).run(t); err == nil {
		t.Error("a code was added to a proof made without one")
	}
}

func TestACodeSignedForOneKeyIsNoUseToAnother(t *testing.T) {
	if _, _, err := (handshake{protocol: Version, hub: address, key: newKey(t), code: "egzo-link-abc", signer: newKey(t)}).run(t); err == nil {
		t.Fatal("a proof of one key was taken for another")
	}
}

func TestWhatIsSignedTellsTheHubFromTheCode(t *testing.T) {
	nonce, key := []byte("nonce"), make(ed25519.PublicKey, ed25519.PublicKeySize)
	if bytes.Equal(Signed("https://hub", nonce, key, "0\ncode"), Signed("https://hub\n0\ncode", nonce, key, "")) {
		t.Error("two different things are signed the same")
	}
}

func TestAHelloOfAnyLengthIsNotReadByAStranger(t *testing.T) {
	link, hub := pair(t)
	go func() {
		stream, err := link.OpenStream()
		if err != nil {
			return
		}
		_, _ = stream.Write(bytes.Repeat([]byte("x"), 2*maxHello))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Handshake(ctx, hub, address); err == nil || !strings.Contains(err.Error(), "refusing the stream") {
		t.Fatalf("err = %v", err)
	}
}

func TestAStrangerWhoSaysNothingGetsTheTimeItWasGiven(t *testing.T) {
	link, hub := pair(t)
	go func() { _, _ = link.OpenStream() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := Handshake(ctx, hub, address); err == nil {
		t.Fatal("a link that said nothing was taken")
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("it held its slot for %v", took)
	}
}

func TestASignatureForAnotherHubIsRefused(t *testing.T) {
	if _, _, err := (handshake{protocol: Version, hub: "https://evil.example.org", key: newKey(t)}).run(t); err == nil {
		t.Fatal("a proof made for another hub was accepted: a hub could log in elsewhere as its links")
	}
}

func TestAnotherVersionIsToldWhichSideToUpgrade(t *testing.T) {
	for protocol, want := range map[int]string{Version + 1: "upgrade the hub", Version - 1: "upgrade the project"} {
		_, greeting, err := handshake{protocol: protocol, hub: address, key: newKey(t)}.run(t)
		if err == nil || !strings.Contains(greeting.Refused, want) {
			t.Errorf("protocol %d: greeting %+v, err %v", protocol, greeting, err)
		}
	}
}

func TestALongLineIsRefused(t *testing.T) {
	long := strings.Repeat("x", MaxLine+1) + "\n"
	var v any
	if err := NewLines(struct {
		io.Reader
		io.Writer
	}{strings.NewReader(long), io.Discard}).Read(&v); err == nil {
		t.Error("a line over the limit was read")
	}
}

func TestAFrameOverTheLimitIsRefused(t *testing.T) {
	frame := []byte{frameInput, 0xff, 0xff, 0xff, 0xff}
	if err := ReadFrames(bytes.NewReader(frame), io.Discard, func(Size) {}); err == nil {
		t.Error("a frame that claims 4 GiB was read")
	}
}

func TestALongPasteIsCutIntoFrames(t *testing.T) {
	var wire bytes.Buffer
	paste := bytes.Repeat([]byte("x"), 3*maxFrame+1)
	if err := WriteInput(&wire, paste); err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	if err := ReadFrames(&wire, &in, func(Size) {}); !errors.Is(err, io.EOF) || !bytes.Equal(in.Bytes(), paste) {
		t.Errorf("%d bytes, err %v", in.Len(), err)
	}
}

func TestWhatAHubSaysCannotDriveATerminal(t *testing.T) {
	cases := []Status{
		{State: StateApproved, Hub: "https://hub", Project: "p\x1b[2J", Owner: "o\nlinked to another"},
		{State: StateRevoked, Hub: "https://hub", Error: "no\u009b31m"},
		{State: StateUnreachable, Error: "refused\r\x1b[1A"},
	}
	for _, status := range cases {
		text := status.Text()
		if strings.ContainsAny(text, "\x1b\x07\n\r\u009b") {
			t.Errorf("%+v says %q", status, text)
		}
	}
}

func TestClosedSaysTheConnectionEnded(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{session: hub}
	select {
	case <-peer.Closed():
		t.Fatal("closed while it runs")
	default:
	}
	link.Close()
	select {
	case <-peer.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the end of the connection was not noticed")
	}
}
