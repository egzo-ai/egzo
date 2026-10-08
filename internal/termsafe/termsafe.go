// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package termsafe makes text that an agent wrote safe to print on a person's terminal. An agent
// chooses its status line, its messages and its answers; printed raw, an escape sequence in them could
// move the cursor, retitle the window, or write the clipboard (OSC 52), and a newline could forge rows
// of a table.
package termsafe

import "strings"

func drop(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// Line is for one cell of a table: every run of whitespace and control characters becomes one space.
func Line(text string) string {
	var b strings.Builder
	space := false
	for _, r := range text {
		switch {
		case drop(r) || r == ' ' || r == 0xa0 || r == 0x2028 || r == 0x2029:
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Block is for text printed as it is: line breaks and tabs stay, every other control character goes.
func Block(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case drop(r):
			return -1
		}
		return r
	}, text)
}

// Truncate cuts text to at most n runes, ending with "..." when it was cut.
func Truncate(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n-3]) + "..."
}
