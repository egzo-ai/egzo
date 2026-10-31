// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/seal"
)

// broken is a control stream that has ended.
type broken struct{}

func (broken) Read([]byte) (int, error)  { return 0, io.EOF }
func (broken) Write([]byte) (int, error) { return 0, errors.New("session shutdown") }

// notes is a control stream that takes what the link writes and never answers.
type notes struct{}

func (notes) Read([]byte) (int, error)    { select {} }
func (notes) Write(p []byte) (int, error) { return len(p), nil }

// heard is a control stream that collects what the link writes.
type heard struct{ said chan string }

func (h heard) Read([]byte) (int, error) { select {} }
func (h heard) Write(p []byte) (int, error) {
	h.said <- string(p)
	return len(p), nil
}

func TestUnlinkWaitsForAConnectionThatIsComingBack(t *testing.T) {
	l := New(Options{Project: "web", Hub: "https://hub"})
	l.control = hublink.NewLines(broken{})
	result := make(chan error, 1)
	go func() {
		state, err := l.Unlink(context.Background())
		if err == nil && state != hublink.StateUnlinked {
			err = errors.New("it ended " + state)
		}
		result <- err
	}()
	time.Sleep(20 * time.Millisecond) // the session is down: the write failed, and it waits

	stream := heard{said: make(chan string, 1)}
	l.mu.Lock()
	l.control = hublink.NewLines(stream)
	l.mu.Unlock()
	l.record(hublink.Verdict{State: hublink.StateApproved}) // a new connection's first verdict

	var message hublink.Unlink
	if err := json.Unmarshal([]byte(<-stream.said), &message); err != nil || !message.Unlink {
		t.Fatalf("the link said %+v, %v", message, err)
	}
	l.record(hublink.Verdict{State: hublink.StateUnlinked})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkOfAProjectTheHubTurnedAwayEndsAtOnce(t *testing.T) {
	for _, state := range []string{hublink.StateRevoked, hublink.StateUnregistered} {
		l := New(Options{Project: "web", Hub: "https://hub"})
		l.record(hublink.Verdict{State: state})
		began := time.Now()
		got, err := l.Unlink(context.Background())
		if err != nil || got != state || time.Since(began) > time.Second {
			t.Errorf("%s: %q, %v, after %v", state, got, err, time.Since(began))
		}
	}
}

func TestAnUnlinkThatTheHubRevokedOrWhoseAnswerWasLostEndsTooAndSaysWhich(t *testing.T) {
	for heard, want := range map[string]string{hublink.StateRevoked: hublink.StateRevoked, hublink.StateUnregistered: hublink.StateUnlinked} {
		l := New(Options{Project: "web", Hub: "https://hub"})
		l.record(hublink.Verdict{State: hublink.StateApproved})
		stream := notes{}
		l.mu.Lock()
		l.control = hublink.NewLines(stream)
		l.mu.Unlock()
		result := make(chan string, 1)
		go func() {
			state, _ := l.Unlink(context.Background())
			result <- state
		}()
		time.Sleep(10 * time.Millisecond)
		l.record(hublink.Verdict{State: heard})
		select {
		case got := <-result:
			if got != want {
				t.Errorf("after %s: %q", heard, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("after %s it kept waiting", heard)
		}
	}
}

func TestAnUnlinkNobodyAnswersGivesUp(t *testing.T) {
	unlinkAnswer = 20 * time.Millisecond
	defer func() { unlinkAnswer = 10 * time.Second }()
	l := New(Options{Project: "web", Hub: "https://hub"})
	l.record(hublink.Verdict{State: hublink.StateApproved})
	l.control = hublink.NewLines(notes{})
	if _, err := l.Unlink(context.Background()); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterOfALinkThatIsLockedOrRegisteredSendsNoCode(t *testing.T) {
	l := New(Options{Project: "web", Hub: "https://hub"})
	if _, err := l.Register(context.Background(), "egzo-link-x"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("locked: %v", err)
	}
	l.record(hublink.Verdict{State: hublink.StateApproved})
	status, err := l.Register(context.Background(), "egzo-link-x")
	if err != nil || status.State != hublink.StateApproved || l.asked != nil {
		t.Errorf("registered: %+v, %v, asked %+v", status, err, l.asked)
	}
}

func TestACodeIsDroppedWhenTheHubDoesNotAnswer(t *testing.T) {
	registerWait = 20 * time.Millisecond
	defer func() { registerWait = dialLimit + 5*time.Second }()
	l := New(Options{Project: "web", Hub: "https://hub"})
	l.record(hublink.Verdict{State: hublink.StateUnregistered})
	if _, err := l.Register(context.Background(), "egzo-link-x"); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err = %v", err)
	}
	if l.current() != nil {
		t.Error("the code is still held")
	}
	if strings.Contains(fmt.Sprintf("%+v", l.Status()), "egzo-link-x") {
		t.Error("the status shows the code")
	}
}

func TestUnlinkGivesUpOnAHubThatStaysAway(t *testing.T) {
	unlinkWait = 20 * time.Millisecond
	defer func() { unlinkWait = 5 * time.Second }()
	_, err := New(Options{Project: "web", Hub: "https://hub"}).Unlink(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("err = %v", err)
	}
}

func keysOf(t *testing.T, secret string) *seal.Keys {
	t.Helper()
	keys, err := seal.New([]byte(strings.Repeat(secret, 32)), "web")
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestTheKeyIsMadeOnceSealedAndComesBack(t *testing.T) {
	dir := t.TempDir()
	first, err := loadKey(dir, keysOf(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadKey(dir, keysOf(t, "a"))
	if err != nil || !first.Equal(second) {
		t.Fatalf("the key changed: %v", err)
	}
	stored, _ := os.ReadFile(filepath.Join(dir, keyFile))
	if bytes.Contains(stored, first.Seed()) {
		t.Error("the key is on the volume in clear")
	}
}

func TestAKeySealedWithAnotherSecretKeyIsRefusedAndKept(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadKey(dir, keysOf(t, "a")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, keyFile))
	if _, err := loadKey(dir, keysOf(t, "b")); err == nil || !strings.Contains(err.Error(), "another secret key") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, keyFile))
	if !bytes.Equal(before, after) {
		t.Error("a wrong key changed the volume")
	}
}

func TestRekeyMovesTheKeyToTheNewSecretKey(t *testing.T) {
	dir := t.TempDir()
	secret := func(c string) []byte { return []byte(strings.Repeat(c, 32)) }
	l := New(Options{Project: "web", Hub: "https://hub", Dir: dir, Engine: &Engine{}})
	if err := l.Unlock(secret("a"), ""); err != nil {
		t.Fatal(err)
	}
	if err := l.Rekey(secret("b")); err != nil {
		t.Fatal(err)
	}
	moved, err := loadKey(dir, keysOf(t, "b"))
	if err != nil || !moved.Equal(l.key) {
		t.Fatalf("the key did not move: %v", err)
	}
	if _, err := loadKey(dir, keysOf(t, "a")); !strings.Contains(fmt.Sprint(err), "another secret key") {
		t.Errorf("the old secret key still opens it: %v", err)
	}
}

func TestALinkThatRestartedCannotRekey(t *testing.T) {
	if err := New(Options{Project: "web", Hub: "https://hub", Dir: t.TempDir()}).Rekey([]byte(strings.Repeat("b", 32))); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheWrongSecretKeyDoesNotLockALinkThatHoldsItsKey(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{Project: "web", Hub: "https://hub", Dir: dir, Engine: &Engine{}})
	if err := l.Unlock([]byte(strings.Repeat("a", 32)), ""); err != nil {
		t.Fatal(err)
	}
	l.record(hublink.Verdict{State: hublink.StateApproved})
	if err := l.Unlock([]byte(strings.Repeat("b", 32)), ""); err == nil {
		t.Fatal("the wrong key was taken")
	}
	if got := l.Status().State; got != hublink.StateApproved {
		t.Errorf("state = %s", got)
	}
}

func TestALogFullOfEscapesFitsAReply(t *testing.T) {
	log := strings.Repeat("\x1b[31m<first>\n", 1<<18) + "the end\n"
	got := tailThatFits(log)
	encoded, _ := json.Marshal(got)
	if len(encoded) > maxResult {
		t.Fatalf("%d bytes as JSON", len(encoded))
	}
	if len(got) == 0 || !strings.HasSuffix(got, "the end\n") || !strings.HasPrefix(got, "\x1b[31m") {
		t.Errorf("kept %d bytes, starting %q", len(got), got[:min(len(got), 10)])
	}
	if short := "a short log\n"; tailThatFits(short) != short {
		t.Error("a log that fits changed")
	}
}

func TestAResultTooLongForALineIsSaid(t *testing.T) {
	if result, failure := encodeResult("spec", []byte(strings.Repeat("x", maxResult))); result != nil || !strings.Contains(failure, "spec") {
		t.Errorf("%d bytes, %q", len(result), failure)
	}
	result, failure := encodeResult("spec", []byte(strings.Repeat("x", maxResult/2)))
	if failure != "" {
		t.Fatal(failure)
	}
	var out bytes.Buffer
	lines := hublink.NewLines(&out)
	if err := lines.Write(hublink.Reply{Done: true, Result: result}); err != nil {
		t.Fatal(err)
	}
	var reply hublink.Reply
	if err := lines.Read(&reply); err != nil {
		t.Errorf("the hub would refuse it: %v", err)
	}
}

// sessions is the two ends of a connection: the hub's and the link's.
func sessions(t *testing.T) (hub, link *yamux.Session) {
	t.Helper()
	a, b := net.Pipe()
	config := yamux.DefaultConfig()
	config.LogOutput = io.Discard
	hub, err := yamux.Server(a, config)
	if err != nil {
		t.Fatal(err)
	}
	link, err = yamux.Client(b, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.Close(); link.Close() })
	return hub, link
}

// ask opens a stream and sends a verb.
func ask(t *testing.T, hub *yamux.Session, verb string) (*yamux.Stream, *hublink.Lines) {
	t.Helper()
	stream, err := hub.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	lines := hublink.NewLines(stream)
	if err := lines.Write(hublink.Request{Verb: verb}); err != nil {
		t.Fatal(err)
	}
	return stream, lines
}

func TestAVerbThatCameWithoutAnApprovalNeverRuns(t *testing.T) {
	hub, session := sessions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var approval atomic.Pointer[context.Context]
	go New(Options{Project: "web", Hub: "https://hub"}).serve(ctx, session, &approval)

	stale, staleLines := ask(t, hub, "stale")
	stale.SetDeadline(time.Now().Add(100 * time.Millisecond))
	var reply hublink.Reply
	if err := staleLines.Read(&reply); !errors.Is(err, yamux.ErrTimeout) {
		t.Fatalf("a project that is not approved said %+v, %v", reply, err)
	}

	approval.Store(&ctx)
	stale.SetDeadline(time.Now().Add(100 * time.Millisecond))
	if err := staleLines.Read(&reply); !errors.Is(err, yamux.ErrTimeout) {
		t.Fatalf("the verb from before the approval ran: %+v, %v", reply, err)
	}
	stream, lines := ask(t, hub, "fresh")
	stream.SetDeadline(time.Now().Add(5 * time.Second))
	if err := lines.Read(&reply); err != nil || !strings.Contains(reply.Error, `no verb "fresh"`) {
		t.Fatalf("an approved project said %+v, %v", reply, err)
	}
}

func TestAStreamLeftWithoutAnApprovalIsClosed(t *testing.T) {
	ignoreFor = 20 * time.Millisecond
	defer func() { ignoreFor = time.Minute }()
	hub, session := sessions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var approval atomic.Pointer[context.Context]
	go New(Options{Project: "web", Hub: "https://hub"}).serve(ctx, session, &approval)
	stream, lines := ask(t, hub, "project")
	stream.SetDeadline(time.Now().Add(5 * time.Second))
	var reply hublink.Reply
	if err := lines.Read(&reply); !errors.Is(err, io.EOF) {
		t.Errorf("got %+v, %v", reply, err)
	}
}
