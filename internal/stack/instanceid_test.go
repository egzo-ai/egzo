// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"regexp"
	"testing"
	"time"
)

func TestAnInstanceIdIsAULIDThatSortsByTime(t *testing.T) {
	zeros := func(b []byte) (int, error) { clear(b); return len(b), nil }
	ones := func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0xff
		}
		return len(b), nil
	}
	// The examples of the ULID spec: the time 1469918176385 and the extremes of the random part.
	at := time.UnixMilli(1469918176385)
	if got := newULID(at, zeros); got != "01ARYZ6S410000000000000000" {
		t.Errorf("zeros = %s", got)
	}
	if got := newULID(at, ones); got != "01ARYZ6S41ZZZZZZZZZZZZZZZZ" {
		t.Errorf("ones = %s", got)
	}
	if earlier, later := newULID(at, ones), newULID(at.Add(time.Millisecond), zeros); earlier >= later {
		t.Errorf("%s does not sort before %s", earlier, later)
	}
	form := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := NewInstanceID()
		if !form.MatchString(id) || seen[id] {
			t.Fatalf("id %q is not a fresh ULID", id)
		}
		seen[id] = true
	}
}
