// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package names

import (
	"strings"
	"testing"
)

func TestAnIdentifierIsAPlainLowercaseNameOfAtMost63Characters(t *testing.T) {
	for _, good := range []string{"a", "coder-1", "my_project", "0x", strings.Repeat("x", 63)} {
		if !Identifier(good) {
			t.Errorf("%q is refused", good)
		}
	}
	for _, bad := range []string{"", "-a", "_a", "A", "a.b", "a b", "a/b", "../etc", "\xff", strings.Repeat("x", 64)} {
		if Identifier(bad) {
			t.Errorf("%.20q is taken", bad)
		}
	}
}

func TestAnActorIsTheOperatorOrOneOfTheKindsAPlaceTakes(t *testing.T) {
	cases := []struct {
		actor string
		kinds []string
		want  bool
	}{
		{"operator", nil, true},
		{"user:alice@example.com", []string{"user"}, true},
		{"service:ci", []string{"user", "service"}, true},
		{"agent:coder-1", []string{"user"}, false},
		{"user:", []string{"user"}, false},
		{"user:-x", []string{"user"}, false},
		{"user:" + strings.Repeat("a", 128), []string{"user"}, true},
		{"user:" + strings.Repeat("a", 129), []string{"user"}, false},
		{"someone", []string{"user"}, false},
	}
	for _, c := range cases {
		if got := Actor(c.actor, c.kinds...); got != c.want {
			t.Errorf("Actor(%.30q, %v) = %v", c.actor, c.kinds, got)
		}
	}
}

func TestIdsAndHashesHaveTheFormEgzoMakesThemIn(t *testing.T) {
	if !InstanceID("01JAB3C4D5E6F7G8H9JKMNPQRS") || InstanceID("01jab3c4d5e6f7g8h9jkmnpqrs") || InstanceID("01JAB3C4D5E6F7G8H9JKMNPQRI") {
		t.Error("instance ids")
	}
	if !Hash(strings.Repeat("ab", 32)) || Hash(strings.Repeat("AB", 32)) || Hash("abc") {
		t.Error("hashes")
	}
}

func TestAHostIsAHostNameOrAnIPAddress(t *testing.T) {
	for _, good := range []string{"example.com", "localhost", "_dmarc.example.com", "192.168.1.10", "::1", "2001:db8::1", strings.Repeat("a", 63) + ".com",
		strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)} {
		if !Host(good) {
			t.Errorf("%.40q is refused", good)
		}
	}
	for _, bad := range []string{"", "a..b", ".example.com", "example.com.", "Example.com", "exa mple.com", "*.example.com", "ex/ample.com",
		"example.com:443", "fe80::1%eth0", strings.Repeat("a", 64) + ".com", strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 62)} {
		if Host(bad) {
			t.Errorf("%.40q is taken", bad)
		}
	}
}

func TestAHostHeaderIsAHostWithAnOptionalPort(t *testing.T) {
	for header, want := range map[string]string{
		"example.com": "example.com", "Example.COM.": "example.com", "example.com:443": "example.com", "[::1]": "::1", "[2001:DB8::1]:443": "2001:db8::1", "[::ffff:1.2.3.4]": "::ffff:1.2.3.4",
		"1.2.3.4:80": "1.2.3.4", "a_b.example": "a_b.example", "example.com:": "example.com", "[::1]:": "::1",
		"": "!", "evil@example.com": "!", "example.com@other": "!", "example.com:443/x": "!", "a..b": "!", strings.Repeat("x", 64) + ".com": "!",
		"example.com:80:80": "!", "a b": "!", ":443": "!", "example.com/x": "!", "[::1]@x": "!", "[::1": "!", "[]": "!", "[example.com]": "!", "[::1]x": "!", "[::1]:80x": "!", "[::g]": "!", "[::1]/x": "!",
	} {
		got, ok := HostHeader(header)
		if !ok {
			got = "!"
		}
		if got != want {
			t.Errorf("HostHeader(%q) = %q, want %q", header, got, want)
		}
	}
}
