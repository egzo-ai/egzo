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
