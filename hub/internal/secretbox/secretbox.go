// Package secretbox encrypts small values at rest with a key from the environment (AES-256-GCM).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// Box seals and opens values with one key.
type Box struct{ aead cipher.AEAD }

// New derives the key from secret, which must be long enough to be a key and not a word.
func New(secret string) (*Box, error) {
	if len(secret) < 16 {
		return nil, errors.New("EGZO_SECRET_KEY must be at least 16 characters (use a random string, e.g. `openssl rand -hex 32`)")
	}
	key := sha256.Sum256([]byte("egzo-hub secretbox v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain; label binds the value to what it belongs to, so a sealed value cannot be moved to another row.
func (b *Box) Seal(label string, plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(label)), nil
}

// Open decrypts what Seal made for the same label.
func (b *Box) Open(label string, sealed []byte) ([]byte, error) {
	size := b.aead.NonceSize()
	if len(sealed) < size {
		return nil, errors.New("sealed value is too short")
	}
	plain, err := b.aead.Open(nil, sealed[:size], sealed[size:], []byte(label))
	if err != nil {
		return nil, errors.New("cannot decrypt: wrong EGZO_SECRET_KEY, or the value was moved")
	}
	return plain, nil
}
