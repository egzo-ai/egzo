// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import "bytes"

// withoutClipboardWrites is the program's recent output as a client that attaches gets it, without the clipboard writes in it
// (OSC 52: "ESC ] 52 ; ...", ended by BEL or ST). A clipboard write is something the program did, not part of the screen: replayed,
// it would copy stale text again on every attach, in a terminal or a browser. One that is not ended yet is kept: the program is
// writing it now, and its end reaches the client live.
func withoutClipboardWrites(output []byte) []byte {
	start := []byte("\x1b]52;")
	var out []byte
	removed := false
	rest := output
	for {
		i := bytes.Index(rest, start)
		if i < 0 {
			break
		}
		end := clipboardWriteEnd(rest[i+len(start):])
		if end < 0 {
			break
		}
		out = append(out, rest[:i]...)
		rest = rest[i+len(start)+end:]
		removed = true
	}
	if !removed {
		return output
	}
	return append(out, rest...)
}

// clipboardWriteEnd is where the sequence whose body begins s ends, past its terminator, or -1 when it does not end in s.
func clipboardWriteEnd(s []byte) int {
	for i, b := range s {
		switch {
		case b == 0x07:
			return i + 1
		case b == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		case b == 0x1b && i+1 == len(s):
			return -1
		}
	}
	return -1
}
