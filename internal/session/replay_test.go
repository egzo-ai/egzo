// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import "testing"

func TestTheReplayLeavesOutClipboardWritesAndNothingElse(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"none", "READY\r\n\x1b]8;;http://x/\x07link\x1b]8;;\x07", "READY\r\n\x1b]8;;http://x/\x07link\x1b]8;;\x07"},
		{"ended by BEL", "a\x1b]52;c;aGVsbG8=\x07b", "ab"},
		{"ended by ST", "a\x1b]52;c;aGVsbG8=\x1b\\b", "ab"},
		{"a query", "a\x1b]52;c;?\x07b", "ab"},
		{"several", "\x1b]52;c;eA==\x07x\x1b]52;p;eQ==\x1b\\y\x1b]52;c;eg==\x07", "xy"},
		{"other OSCs stay", "\x1b]0;title\x07\x1b]52;c;eA==\x07\x1b]522;z\x07", "\x1b]0;title\x07\x1b]522;z\x07"},
		{"one being written stays", "a\x1b]52;c;aGVs", "a\x1b]52;c;aGVs"},
		{"one cut in its terminator stays", "a\x1b]52;c;aGVsbG8=\x1b", "a\x1b]52;c;aGVsbG8=\x1b"},
		{"after one that ended", "\x1b]52;c;eA==\x07a\x1b]52;c;eQ", "a\x1b]52;c;eQ"},
	} {
		if got := string(withoutClipboardWrites([]byte(c.in))); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
