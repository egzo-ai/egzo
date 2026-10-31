// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/seal"
)

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
