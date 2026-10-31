// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/seal"
)

// broken is a control stream that has ended.
type broken struct{}

func (broken) Read([]byte) (int, error)  { return 0, io.EOF }
func (broken) Write([]byte) (int, error) { return 0, errors.New("session shutdown") }

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
	go func() { result <- l.Unlink(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // the session is down: the write failed, and it waits

	stream := heard{said: make(chan string, 1)}
	verdicts := make(chan hublink.Verdict, 1)
	l.mu.Lock()
	l.control, l.verdict = hublink.NewLines(stream), verdicts
	l.mu.Unlock()
	l.set(func(*Status) {}) // a new connection's first verdict

	var message hublink.Unlink
	if err := json.Unmarshal([]byte(<-stream.said), &message); err != nil || !message.Unlink {
		t.Fatalf("the link said %+v, %v", message, err)
	}
	verdicts <- hublink.Verdict{State: hublink.StateUnlinked}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkGivesUpOnAHubThatStaysAway(t *testing.T) {
	unlinkWait = 20 * time.Millisecond
	defer func() { unlinkWait = 5 * time.Second }()
	err := New(Options{Project: "web", Hub: "https://hub"}).Unlink(context.Background())
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
