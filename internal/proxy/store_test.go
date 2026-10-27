// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/egzo-ai/egzo/internal/seal"
)

func sealKeysFor(t *testing.T, secret string) *seal.Keys {
	t.Helper()
	keys, err := seal.New([]byte(secret+strings.Repeat("-", seal.MinKeyBytes)), "shop")
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// nothingInClear fails when a file of dir holds a private key in clear.
func nothingInClear(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		if bytes.Contains(data, []byte("PRIVATE KEY")) {
			t.Errorf("%s holds a private key in clear", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("left %s behind", entry.Name())
		}
		if info, _ := entry.Info(); strings.HasSuffix(entry.Name(), ".sealed") && info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v", entry.Name(), info.Mode().Perm())
		}
	}
}

func TestAStoreIsMadeSealedAndOpensAgainWithTheSameKeyOnly(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	if store.CA() != nil {
		t.Fatal("a new store has a CA before it has a key")
	}
	if changed, err := store.Unlock(sealKeysFor(t, "one")); err != nil || !changed {
		t.Fatalf("Unlock = %v, %v", changed, err)
	}
	if changed, _ := store.Unlock(sealKeysFor(t, "one")); changed {
		t.Error("the same key again changed the store")
	}
	made := store.CA()
	hostKey, _ := store.SSHHostKey()
	nothingInClear(t, dir)

	restarted := openStore(t, dir)
	if ca := restarted.CA(); ca == nil || ca.Key != nil || ca.Fingerprint() != made.Fingerprint() {
		t.Fatalf("before the key, a restarted store knows the certificate only: %+v", ca)
	}
	if signer, public := restarted.SSHHostKey(); signer != nil || !bytes.Equal(public.Marshal(), hostKey.PublicKey().Marshal()) {
		t.Fatal("before the key, a restarted store knows the public ssh host key only")
	}
	if _, err := restarted.Unlock(sealKeysFor(t, "two")); !errors.Is(err, ErrWrongSecretKey) {
		t.Fatalf("another key: %v", err)
	}
	if restarted.CA().Key != nil {
		t.Fatal("another key opened something")
	}
	if _, err := restarted.Unlock(sealKeysFor(t, "one")); err != nil {
		t.Fatal(err)
	}
	if restarted.CA().Fingerprint() != made.Fingerprint() || restarted.CA().Key == nil {
		t.Error("the right key did not open the same CA")
	}
	if signer, _ := restarted.SSHHostKey(); signer == nil || !bytes.Equal(signer.PublicKey().Marshal(), hostKey.PublicKey().Marshal()) {
		t.Error("the right key did not open the same ssh host key")
	}
}

func TestAClearStoreFromBeforeTheSecretKeyKeepsItsKeysAndIsSealedByTheFirstKey(t *testing.T) {
	dir := t.TempDir()
	ca, _ := NewCA()
	keyDER, _ := x509.MarshalECPrivateKey(ca.Key)
	os.WriteFile(filepath.Join(dir, caKeyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	os.WriteFile(filepath.Join(dir, caCertFile), ca.CertPEM, 0o644)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(private, "egzo proxy")
	os.WriteFile(filepath.Join(dir, sshHostKeyFile), pem.EncodeToMemory(block), 0o600)
	signer, _ := ssh.NewSignerFromKey(private)

	store := openStore(t, dir)
	if store.CA() == nil || store.CA().Key == nil {
		t.Fatal("a clear CA is usable before the key, as it was")
	}
	if _, err := store.Unlock(sealKeysFor(t, "one")); err != nil {
		t.Fatal(err)
	}
	nothingInClear(t, dir)
	reopened := openStore(t, dir)
	reopened.Unlock(sealKeysFor(t, "one"))
	if reopened.CA().Fingerprint() != ca.Fingerprint() {
		t.Error("the CA changed: every agent would have to learn a new one")
	}
	if got, _ := reopened.SSHHostKey(); got == nil || !bytes.Equal(got.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Error("the ssh host key changed")
	}
}

func TestRotatingMovesTheStoreToAnotherKey(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	store.Unlock(sealKeysFor(t, "one"))
	old := store.CA()
	rotated, _, err := store.Rotate(sealKeysFor(t, "two"), func(*CA) error { return nil })
	if err != nil || rotated.Fingerprint() == old.Fingerprint() {
		t.Fatalf("Rotate = %v", err)
	}
	nothingInClear(t, dir)
	if _, err := openStore(t, dir).Unlock(sealKeysFor(t, "one")); !errors.Is(err, ErrWrongSecretKey) {
		t.Errorf("the old key: %v", err)
	}
	reopened := openStore(t, dir)
	if _, err := reopened.Unlock(sealKeysFor(t, "two")); err != nil || reopened.CA().Fingerprint() != rotated.Fingerprint() {
		t.Errorf("the new key: %v", err)
	}
}

func TestAnIncompleteOrMismatchedCAIsNeverReplacedSilently(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	store.Unlock(sealKeysFor(t, "one"))
	os.Remove(filepath.Join(dir, caSealedFile))
	if _, err := OpenStore(dir); err == nil {
		t.Error("a certificate without its key was accepted")
	}

	dir = t.TempDir()
	a, _ := NewCA()
	b, _ := NewCA()
	keyDER, _ := x509.MarshalECPrivateKey(b.Key)
	os.WriteFile(filepath.Join(dir, caKeyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	os.WriteFile(filepath.Join(dir, caCertFile), a.CertPEM, 0o644)
	if _, err := OpenStore(dir); err == nil || !strings.Contains(err.Error(), "match") {
		t.Errorf("a key that is not the certificate's: %v", err)
	}

	dir = t.TempDir()
	openStore(t, dir).Unlock(sealKeysFor(t, "one"))
	os.WriteFile(filepath.Join(dir, caSealedFile), []byte("truncated"), 0o600)
	if _, err := openStore(t, dir).Unlock(sealKeysFor(t, "one")); err == nil || errors.Is(err, ErrWrongSecretKey) {
		t.Errorf("a corrupt sealed key: %v", err)
	}
}
