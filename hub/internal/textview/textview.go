// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package textview decides whether the start of a file is text a person can read in the hub, and makes it ready to show as text. What
// it is given was written by an agent and is not trusted: the decision is made on the bytes, never on the file's name. The result is
// only ever shown as text (a string in a <pre>), never as a page.
package textview

import (
	"bytes"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limit is the most of a file the hub shows: the first 256 KiB.
const Limit = 256 << 10

// MaxLine is the longest a line is shown, in bytes; a longer one (minified code, a data blob) is cut.
const MaxLine = 10_000

// Result is what a file is shown as: Text, or why not (Binary).
type Result struct {
	Text string
	// Binary says why the file is not shown: the format it starts with, or what it is not. "" for text.
	Binary string
	// CutLines counts the lines cut at MaxLine. Bidi counts the characters that change the direction of text (U+202A–U+202E,
	// U+2066–U+2069 and the like): text can read otherwise than it is, so the viewer shows them.
	CutLines, Bidi int
}

// Inspect reads data, the start of a file; truncated says the file goes on past it, so that a character it cuts is not taken for a
// broken one.
func Inspect(data []byte, truncated bool) Result {
	if truncated {
		data = dropCutRune(data)
	}
	// the format a file starts with: a PNG, a zip, a PDF named .md is what its bytes say (the first 512 are sniffed)
	sniffed := http.DetectContentType(data)
	if len(data) > 0 && !strings.HasPrefix(sniffed, "text/") {
		return Result{Binary: sniffed}
	}
	if len(data) > 0 && !strings.HasSuffix(sniffed, "charset=utf-8") {
		return Result{Binary: "text that is not UTF-8 (" + strings.TrimPrefix(sniffed, "text/plain; charset=") + ")"}
	}
	// the sniffer reads 512 bytes: the rest is read here, every byte
	if !utf8.Valid(data) {
		return Result{Binary: "not UTF-8 text"}
	}
	if bytes.ContainsFunc(data, binaryControl) {
		return Result{Binary: "binary data"}
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF")) // a byte order mark says nothing to a reader
	var r Result
	var text strings.Builder
	text.Grow(len(data))
	for len(data) > 0 {
		line, rest, found := bytes.Cut(data, []byte("\n"))
		if len(line) > MaxLine {
			line = dropCutRune(line[:MaxLine])
			r.CutLines++
		}
		text.Write(line)
		if found {
			text.WriteByte('\n')
		}
		data = rest
	}
	r.Text = text.String()
	for _, c := range r.Text {
		if unicode.Is(unicode.Bidi_Control, c) {
			r.Bidi++
		}
	}
	return r
}

// binaryControl is a control character that text does not hold: what the sniffer counts as binary (a NUL among them), read over all
// the bytes. Tab, newline, form feed, carriage return and escape (a coloured log) are text.
func binaryControl(c rune) bool {
	return c <= 0x08 || c == 0x0B || 0x0E <= c && c <= 0x1A || 0x1C <= c && c <= 0x1F
}

// dropCutRune drops the end of data if it is the start of a character that data cuts.
func dropCutRune(data []byte) []byte {
	for back := 1; back <= utf8.UTFMax && back <= len(data); back++ {
		if start := len(data) - back; utf8.RuneStart(data[start]) {
			if !utf8.FullRune(data[start:]) {
				return data[:start]
			}
			return data
		}
	}
	return data
}
