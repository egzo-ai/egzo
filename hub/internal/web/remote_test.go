// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import "testing"

func TestARemoteIsShownOnlyAsAnHTTPSURLWithNoSecret(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/egzo-ai/egzo.git": "https://github.com/egzo-ai/egzo.git",
		"https://user:pw@example.com/r.git":   "",
		"https://example.com/r.git?token=x":   "",
		"https://example.com/r.git#x":         "",
		"javascript:alert(1)":                 "",
		"http://example.com/r.git":            "",
		"ssh://git@example.com/r.git":         "",
		"":                                    "",
	} {
		if got := shownRemote(remote); got != want {
			t.Errorf("%q: %q, want %q", remote, got, want)
		}
	}
}
