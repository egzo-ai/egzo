// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// crockford is the alphabet of a ULID: base32 without I, L, O and U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewInstanceID makes the id of an instance: a ULID, 48 bits of milliseconds then 80 random bits in 26 characters, so ids
// sort by the time they were made. Nothing derives it from the name, which can come back; the id never does.
func NewInstanceID() string { return newULID(time.Now(), rand.Reader.Read) }

func newULID(now time.Time, random func([]byte) (int, error)) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[:8], uint64(now.UnixMilli())<<16)
	if _, err := random(raw[6:]); err != nil {
		panic("instance id: no randomness: " + err.Error()) // crypto/rand does not fail on a supported platform
	}
	// 128 bits are 26 groups of 5 from the top, the first group holding the 3 bits that are left over.
	var out [26]byte
	hi, lo := binary.BigEndian.Uint64(raw[:8]), binary.BigEndian.Uint64(raw[8:])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
