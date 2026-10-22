package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Passwords are hashed with argon2id, with the parameters RFC 9106 recommends for a memory-constrained setting, in the usual
// encoded form ($argon2id$v=19$m=...,t=...,p=...$salt$hash) so stronger parameters can be chosen later without breaking old hashes.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var b64 = base64.RawStdEncoding

// hashing bounds the hashes computed at once, each of which takes argonMemory: password sign-ins need no session, so without a
// bound a burst of them would take as much memory as it likes. The others wait their turn.
var hashing = make(chan struct{}, 2)

func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// HashPassword returns the encoded argon2id hash of a password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches an encoded hash, with the parameters the hash names.
func CheckPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id hash")
	}
	var version int
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unknown argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errors.New("bad argon2 parameters")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := idKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is checked against when there is no user or no password, so a failed sign-in costs the same time in every case.
var dummyHash, _ = HashPassword("not a password anyone has")

// CheckNoPassword spends the time of a password check and fails.
func CheckNoPassword(password string) {
	_, _ = CheckPassword(dummyHash, password)
}

// NewPassword makes a random password: 26 characters of base32, 130 bits.
func NewPassword() string { return rand.Text() }
