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
			_ = lines.Write(Proof{Signature: ed25519.Sign(h.key, Signed(h.hub, greeting.Nonce, public))})
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
	peer, greeting, err := handshake{Version, address, newKey(t)}.run(t)
	if err != nil || peer == nil || peer.Hello.Project != "web" || len(greeting.Nonce) != 32 {
		t.Fatalf("peer %+v, greeting %+v, err %v", peer, greeting, err)
	}
}

func TestASignatureForAnotherHubIsRefused(t *testing.T) {
	if _, _, err := (handshake{Version, "https://evil.example.org", newKey(t)}).run(t); err == nil {
		t.Fatal("a proof made for another hub was accepted: a hub could log in elsewhere as its links")
	}
}

func TestAnotherVersionIsToldWhichSideToUpgrade(t *testing.T) {
	for protocol, want := range map[int]string{Version + 1: "upgrade the hub", Version - 1: "upgrade the project"} {
		_, greeting, err := handshake{protocol, address, newKey(t)}.run(t)
		if err == nil || !strings.Contains(greeting.Refused, want) {
			t.Errorf("protocol %d: greeting %+v, err %v", protocol, greeting, err)
		}
	}
}

// answering is a link that answers one request, as the verb's handler does.
func answering(t *testing.T, link *yamux.Session, answer func(Request, *Lines, *yamux.Stream)) {
	t.Helper()
	go func() {
		stream, err := link.AcceptStream()
		if err != nil {
			return
		}
		defer stream.Close()
		lines := NewLines(stream)
		var request Request
		if lines.Read(&request) == nil {
			answer(request, lines, stream)
		}
	}()
}

func TestAVerbGivesItsStepsAndItsResultAndItsError(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{Hello: Hello{Project: "web"}, session: hub}
	answering(t, link, func(q Request, lines *Lines, _ *yamux.Stream) {
		if q.Verb != VerbSpawn || q.Template != "coder" || q.Actor != "user:7" {
			t.Errorf("request %+v", q)
		}
		_ = lines.Write(Reply{Progress: "create container"})
		_ = lines.Write(Reply{Done: true, Result: []byte(`"coder-1"`)})
	})
	var steps []string
	agent, err := peer.Spawn(context.Background(), "coder", "", "user:7", func(s string) { steps = append(steps, s) })
	if err != nil || agent != "coder-1" || len(steps) != 1 {
		t.Fatalf("agent %q, steps %q, err %v", agent, steps, err)
	}

	answering(t, link, func(_ Request, lines *Lines, _ *yamux.Stream) { _ = lines.Write(Reply{Done: true, Error: "no agent"}) })
	if err := peer.Stop(context.Background(), "x"); err == nil || err.Error() != "no agent" {
		t.Errorf("err = %v", err)
	}

	answering(t, link, func(_ Request, lines *Lines, _ *yamux.Stream) { _ = lines.Write(Reply{Done: true, Gone: true}) })
	var gone ErrNoProject
	if _, err := peer.Project(context.Background()); !errors.As(err, &gone) {
		t.Errorf("err = %v", err)
	}
}

func TestAVerbEndsWithItsContext(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{session: hub}
	answering(t, link, func(Request, *Lines, *yamux.Stream) { time.Sleep(time.Minute) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := peer.Project(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestATerminalCarriesOutputInputAndSizes(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{session: hub}
	got := make(chan string, 1)
	sizes := make(chan Size, 1)
	answering(t, link, func(q Request, lines *Lines, stream *yamux.Stream) {
		_ = lines.Write(Reply{Done: true})
		_, _ = stream.Write([]byte("the screen"))
		var in bytes.Buffer
		err := ReadFrames(lines.Rest(), &in, func(s Size) { sizes <- s })
		if !errors.Is(err, io.EOF) {
			t.Errorf("frames: %v", err)
		}
		got <- in.String()
	})
	input, writer := io.Pipe()
	resize := make(chan Size, 1)
	resize <- Size{Cols: 120, Rows: 40}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- peer.Attach(context.Background(), "coder", Terminal{In: input, Out: &out, Resize: resize})
	}()
	if s := <-sizes; s.Cols != 120 {
		t.Errorf("size %+v", s)
	}
	_, _ = writer.Write([]byte("typed"))
	writer.Close()
	if text := <-got; text != "typed" {
		t.Errorf("input %q", text)
	}
	if err := <-done; err != nil || out.String() != "the screen" {
		t.Errorf("output %q, err %v", out.String(), err)
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
