package store

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Usernames are handles the hub chooses once, when it makes a user, from what the provider says: readable, unique across the
// hub, and never changed by the provider afterwards.

const usernameMax = 32

// reserved are names no generated user gets: the starter account, and actors the hub already shows by those names.
var reserved = map[string]bool{StarterUsername: true, "operator": true, "hub": true}

// UsernameFrom picks the first candidate that is not empty once normalized (preferred_username, the local part of the email, the
// name, in that order), and "user" when none is.
func UsernameFrom(candidates ...string) string {
	for _, c := range candidates {
		if n := NormalizeUsername(c); n != "" {
			return n
		}
	}
	return "user"
}

// NormalizeUsername cuts a candidate at "@", drops accents, lowercases, turns every run of other characters than a-z 0-9 . _ - into
// one "-", trims . _ - from both ends and keeps 32 characters at most. A name written only in another script normalizes to "".
func NormalizeUsername(s string) string {
	s, _, _ = strings.Cut(s, "@")
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue // a combining accent, split off by NFKD
		}
		r = unicode.ToLower(r)
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteRune('-')
			dash = true
		}
	}
	return trimHandle(b.String())
}

func trimHandle(s string) string {
	s = strings.Trim(s, "._-")
	if len(s) > usernameMax {
		s = strings.TrimRight(s[:usernameMax], "._-")
	}
	return s
}

// withSuffix is base for n = 1, and base-n after, shortening base so the whole stays within 32 characters.
func withSuffix(base string, n int) string {
	if n == 1 {
		return base
	}
	suffix := "-" + strconv.Itoa(n)
	if len(base)+len(suffix) > usernameMax {
		base = strings.TrimRight(base[:usernameMax-len(suffix)], "._-")
	}
	return base + suffix
}
