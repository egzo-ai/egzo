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
	"sync/atomic"
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
	if peer.control.limit != maxControl {
		t.Errorf("a link that proved its key may send %d bytes on its control stream", peer.control.limit)
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

// silence is a control stream that stays open and says nothing, as a link's does.
type silence struct{}

func (silence) Read([]byte) (int, error)    { select {} }
func (silence) Write(p []byte) (int, error) { return len(p), nil }

// proved is a hub's Peer of a connection, past the handshake, with the link's end of its control stream.
func proved(t *testing.T) (*Peer, *yamux.Session, *Lines) {
	t.Helper()
	link, hub := pair(t)
	stream, err := link.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	lines := NewLines(stream)
	public := newKey(t).Public().(ed25519.PublicKey)
	if err := lines.Write(Hello{Protocol: Version}); err != nil { // yamux tells the hub of a stream with its first bytes
		t.Fatal(err)
	}
	accepted, err := hub.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	control := NewLines(accepted)
	var hello Hello
	if err := control.Read(&hello); err != nil {
		t.Fatal(err)
	}
	control.limit = maxControl
	return &Peer{Hello: Hello{Key: public}, session: hub, control: control}, link, lines
}

func TestALineOverTheControlLimitEndsTheConnection(t *testing.T) {
	peer, _, lines := proved(t)
	go peer.Run(context.Background(), Handlers{})
	if err := lines.Write(Unlink{}); err != nil {
		t.Fatal(err)
	}
	go lines.Write(FeedOpen{Feed: strings.Repeat("x", 2*maxControl)})
	select {
	case <-peer.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed up")
	}
}

func TestControlMessagesAreHeldBack(t *testing.T) {
	controlPause = 30 * time.Millisecond
	defer func() { controlPause = 100 * time.Millisecond }()
	peer, _, lines := proved(t)
	asked := make(chan time.Time, 1)
	go peer.Run(context.Background(), Handlers{Unlink: func() Verdict {
		asked <- time.Now()
		return Verdict{State: StateUnlinked}
	}})
	began := time.Now()
	for range 10 {
		if err := lines.Write(Unlink{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lines.Write(Unlink{Unlink: true}); err != nil {
		t.Fatal(err)
	}
	if took := (<-asked).Sub(began); took < 10*controlPause {
		t.Errorf("11 messages were read in %v", took)
	}
}

func TestTheAnswerToAnUnlinkArrivesAndThenTheConnectionEnds(t *testing.T) {
	peer, _, lines := proved(t)
	asked := make(chan struct{}, 1)
	go peer.Run(context.Background(), Handlers{Unlink: func() Verdict {
		asked <- struct{}{}
		return Verdict{State: StateUnlinked}
	}})
	if err := lines.Write(Unlink{Unlink: true}); err != nil {
		t.Fatal(err)
	}
	<-asked
	var verdict Verdict
	if err := lines.Read(&verdict); err != nil || verdict.State != StateUnlinked {
		t.Fatalf("the link heard %+v, %v", verdict, err)
	}
	select {
	case <-peer.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed up")
	}
}

func TestALinkThatDoesNotHangUpIsClosed(t *testing.T) {
	peer, _, _ := proved(t)
	began := time.Now()
	if err := peer.Finish(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < finishGrace {
		t.Errorf("it was closed after %v, with no time to hear its verdict", took)
	}
	select {
	case <-peer.Closed():
	default:
		t.Error("the connection stayed up")
	}

	peer, link, _ := proved(t)
	done := make(chan struct{})
	go func() { _ = peer.Finish(); close(done) }()
	link.Close()
	<-done
}

func TestAFeedIsPacedInBytesBeforeItIsDecoded(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{session: hub, control: NewLines(silence{})}
	var paced atomic.Int64
	decoded := make(chan int64, 1)
	go peer.Run(context.Background(), Handlers{
		Status: func(StatusLine) {
			select {
			case decoded <- paced.Load():
			default:
			}
		},
		Pace: func(n int) { paced.Add(int64(n)) },
	})
	stream, err := link.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	lines := NewLines(stream)
	if err := lines.Write(FeedOpen{Feed: FeedStatus}); err != nil {
		t.Fatal(err)
	}
	var start FeedStart
	if err := lines.Read(&start); err != nil {
		t.Fatal(err)
	}
	line := StatusLine{Reason: strings.Repeat("x", 2<<20)}
	if err := lines.Write(line); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-decoded:
		if got < 2<<20 {
			t.Errorf("%d bytes were paced before a line of 2 MiB was decoded", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the line never came")
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

func TestALinkHoldsFewStreamsOpenTowardsTheHub(t *testing.T) {
	link, hub := pair(t)
	peer := &Peer{session: hub}
	peer.control = NewLines(silence{})
	go peer.Run(context.Background(), Handlers{})
	var open []*yamux.Stream
	refused := 0
	for range maxFeeds + 3 {
		stream, err := link.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		lines := NewLines(stream)
		if err := lines.Write(FeedOpen{Feed: FeedStatus}); err != nil {
			t.Fatal(err)
		}
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		var start FeedStart
		if err := lines.Read(&start); err != nil {
			refused++
			continue
		}
		open = append(open, stream)
	}
	if len(open) != maxFeeds || refused != 3 {
		t.Errorf("%d streams were served and %d refused, want %d and 3", len(open), refused, maxFeeds)
	}
	open[0].Close() // a feed that ended leaves room for its next try
	// the hub hears of the end when it gets there, so a try can come before the room does, as a link's next try would
	deadline := time.Now().Add(5 * time.Second)
	for {
		again, err := link.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		lines := NewLines(again)
		if err := lines.Write(FeedOpen{Feed: FeedStatus}); err != nil {
			t.Fatal(err)
		}
		again.SetReadDeadline(time.Now().Add(5 * time.Second))
		var start FeedStart
		err = lines.Read(&start)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a feed opened again after one ended: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
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
