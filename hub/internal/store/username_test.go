package store

import (
	"strings"
	"testing"
)

func TestUsernamesAreNormalizedFromTheFirstUsableCandidate(t *testing.T) {
	for _, c := range []struct {
		candidates []string
		want       string
	}{
		{[]string{"alex@corp.com"}, "alex"},                // Entra ID's preferred_username: the domain is never used
		{[]string{"", "Jane.Doe@example.com"}, "jane.doe"}, // the local part of the email
		{[]string{"", "", "Zoë Saldaña"}, "zoe-saldana"},   // accents dropped, spaces become one dash
		{[]string{"李小龙", "", "Bruce"}, "bruce"},            // another script normalizes to nothing: next candidate
		{[]string{"__--x--__"}, "x"},
		{[]string{"a  b!!c"}, "a-b-c"},
		{[]string{"", "", ""}, "user"},
		{[]string{strings.Repeat("a", 40)}, strings.Repeat("a", 32)},
	} {
		if got := UsernameFrom(c.candidates...); got != c.want {
			t.Errorf("UsernameFrom(%q) = %q, want %q", c.candidates, got, c.want)
		}
	}
}

func TestATakenOrReservedUsernameGetsTheSmallestFreeSuffix(t *testing.T) {
	s := open(t)
	s.EnsureStarter(ctx)
	for _, want := range []string{"alex", "alex-2", "alex-3"} {
		u, _, _, err := s.LoginIdentity(ctx, issuer, want+"-sub", Profile{PreferredUsername: "Alex"})
		if err != nil || u.Username != want {
			t.Errorf("got %q, want %q (%v)", u.Username, want, err)
		}
	}
	for _, name := range []string{"operator", "hub"} {
		if u, _, _, _ := s.LoginIdentity(ctx, issuer, name+"-sub", Profile{PreferredUsername: name}); u.Username != name+"-2" {
			t.Errorf("the reserved %q gave %q", name, u.Username)
		}
	}
	long := strings.Repeat("b", 32)
	s.LoginIdentity(ctx, issuer, "long-1", Profile{PreferredUsername: long})
	if u, _, _, _ := s.LoginIdentity(ctx, issuer, "long-2", Profile{PreferredUsername: long}); u.Username != strings.Repeat("b", 30)+"-2" {
		t.Errorf("a long name with a suffix: %q", u.Username)
	}
	// case-insensitive: ALEX is alex
	if u, _, _, _ := s.LoginIdentity(ctx, issuer, "upper", Profile{PreferredUsername: "ALEX"}); u.Username != "alex-4" {
		t.Errorf("got %q", u.Username)
	}
}
