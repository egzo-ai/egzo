// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import "testing"

func replayOf(chunks ...string) string {
	var m modeTracker
	for _, c := range chunks {
		m.feed([]byte(c))
	}
	return string(m.replay())
}

func TestTheModeTrackerRemembersWhatTheProgramLeftOn(t *testing.T) {
	for name, tc := range map[string]struct {
		chunks []string
		want   string
	}{
		"nothing":               {[]string{"plain text \x1b[31mred\x1b[0m"}, ""},
		"mouse and paste":       {[]string{"\x1b[?1000h\x1b[?1006h\x1b[?2004h"}, "\x1b[?1000h\x1b[?1006h\x1b[?2004h"},
		"several in one":        {[]string{"\x1b[?1002;1006h"}, "\x1b[?1002h\x1b[?1006h"},
		"set then reset":        {[]string{"\x1b[?1000h...\x1b[?1000l"}, "\x1b[?1000l"},
		"reset then set again":  {[]string{"\x1b[?1000l\x1b[?1000h"}, "\x1b[?1000h"},
		"split across chunks":   {[]string{"x\x1b", "[?10", "06", "h"}, "\x1b[?1006h"},
		"screen modes ignored":  {[]string{"\x1b[?1049h\x1b[?25l\x1b[?2026h\x1b[?1000h"}, "\x1b[?1000h"},
		"not a private mode":    {[]string{"\x1b[1000h\x1b[?1000"}, ""},
		"an escape inside one":  {[]string{"\x1b[?10\x1b[?1006h"}, "\x1b[?1006h"},
		"first-seen order kept": {[]string{"\x1b[?1006h\x1b[?1000h\x1b[?1006l"}, "\x1b[?1006l\x1b[?1000h"},
	} {
		if got := replayOf(tc.chunks...); got != tc.want {
			t.Errorf("%s: replay = %q, want %q", name, got, tc.want)
		}
	}
}
