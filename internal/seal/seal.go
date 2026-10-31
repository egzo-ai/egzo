// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package seal encrypts what egzo keeps at rest with keys derived from the project's secret key.
//
// A key for one use is HKDF-SHA256 of the secret key, with the project as salt and a label per use
// ("egzo/v1/ca"). A record is sealed with XChaCha20-Poly1305 and a random nonce; its associated data names
// the project, the use and the format version, so a record cannot be moved to another use or project. A key
// check (an HMAC of a fixed label under its own derived key) is stored with each record, so a wrong secret key
// is told apart from a corrupt record.
package seal

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// MinKeyBytes is the shortest secret key egzo accepts.
const MinKeyBytes = 32

const version = 1

// The uses: one derived key each.
const (
	UseCA         = "ca"
	UseSSHHostKey = "ssh-host-key"
	UseOAuth      = "oauth"
	UseLink       = "link"
)

// ErrWrongKey is a record sealed with another secret key (or for another project).
var ErrWrongKey = errors.New("sealed with another secret key")

// ErrCorrupt is a record that cannot be read, or whose data does not open under the right key.
var ErrCorrupt = errors.New("the sealed data is corrupt")

// Keys derives the keys of one project from its secret key.
type Keys struct {
	secret  []byte
	project string
}

// New checks the secret key's length and returns the project's keys.
func New(secret []byte, project string) (*Keys, error) {
	if len(secret) < MinKeyBytes {
		return nil, fmt.Errorf("the secret key is %d bytes, it must be at least %d", len(secret), MinKeyBytes)
	}
	return &Keys{secret: append([]byte(nil), secret...), project: project}, nil
}

func (k *Keys) derive(label string) []byte {
	key, err := hkdf.Key(sha256.New, k.secret, []byte(k.project), "egzo/v1/"+label, chacha20poly1305.KeySize)
	if err != nil {
		panic(err) // only for a length HKDF cannot produce
	}
	return key
}

// check is the key check stored with every record: it depends on the secret key and the project only.
func (k *Keys) check() []byte {
	mac := hmac.New(sha256.New, k.derive("check"))
	mac.Write([]byte("egzo key check"))
	return mac.Sum(nil)
}

func (k *Keys) associated(use string) []byte {
	return fmt.Appendf(nil, "egzo sealed record v%d project=%q use=%q", version, k.project, use)
}

type record struct {
	Version int    `json:"version"`
	Use     string `json:"use"`
	Check   []byte `json:"check"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

// Seal encrypts plain for use.
func (k *Keys) Seal(use string, plain []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.derive(use))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return json.Marshal(record{
		Version: version, Use: use, Check: k.check(), Nonce: nonce,
		Data: aead.Seal(nil, nonce, plain, k.associated(use)),
	})
}

// Open decrypts a record sealed for use. ErrWrongKey says the record was sealed with another secret key;
// ErrCorrupt that it cannot be read.
func (k *Keys) Open(use string, sealed []byte) ([]byte, error) {
	var r record
	if err := json.Unmarshal(sealed, &r); err != nil || r.Version != version {
		return nil, ErrCorrupt
	}
	if !hmac.Equal(r.Check, k.check()) {
		return nil, ErrWrongKey
	}
	if r.Use != use {
		return nil, ErrCorrupt
	}
	aead, err := chacha20poly1305.NewX(k.derive(use))
	if err != nil {
		return nil, err
	}
	if len(r.Nonce) != aead.NonceSize() {
		return nil, ErrCorrupt
	}
	plain, err := aead.Open(nil, r.Nonce, r.Data, k.associated(use))
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// Same reports whether two sets of keys come from the same secret key and project.
func (k *Keys) Same(other *Keys) bool {
	return other != nil && hmac.Equal(k.check(), other.check())
}
