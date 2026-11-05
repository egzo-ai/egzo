// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package textview

import (
	"bytes"
	"strings"
	"testing"
)

func TestTextIsShownAsItIs(t *testing.T) {
	for name, data := range map[string]string{
		"markdown":         "# The plan\n\n- one\n",
		"html is text too": "<!DOCTYPE html><script>alert(1)</script>",
		"a coloured log":   "\x1b[31mred\x1b[0m\r\n\ttabbed\f",
		"accents":          "café, naïve, 日本語\n",
		"empty":            "",
	} {
		r := Inspect([]byte(data), false)
		if r.Binary != "" || r.Text != data {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestTheBytesDecideNeverTheName(t *testing.T) {
	for name, c := range map[string]struct{ data, want string }{
		"a PNG named .md":      {"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", "image/png"},
		"a zip":                {"PK\x03\x04\x14\x00\x00\x00", "application/zip"},
		"a PDF":                {"%PDF-1.7\n", "application/pdf"},
		"gzip":                 {"\x1f\x8b\x08\x00", "application/x-gzip"},
		"UTF-16":               {"\xff\xfeh\x00i\x00", "text that is not UTF-8 (utf-16le)"},
		"Latin-1":              {"caf\xe9 au lait\n", "not UTF-8 text"},
		"a NUL past the sniff": {strings.Repeat("a", 8000) + "\x00b", "binary data"},
		"a control past it":    {strings.Repeat("a\n", 600) + "\x01", "binary data"},
	} {
		if r := Inspect([]byte(c.data), false); r.Binary != c.want || r.Text != "" {
			t.Errorf("%s: %+v, want %q", name, r, c.want)
		}
	}
}

func TestACharacterCutAtTheLimitIsDroppedNotTakenForBinary(t *testing.T) {
	data := []byte(strings.Repeat("a", 10) + "日本") // 3 bytes each
	for cut := 11; cut < len(data); cut++ {
		if cut == 13 {
			continue // a whole character
		}
		r := Inspect(data[:cut], true)
		if r.Binary != "" || !strings.HasPrefix(r.Text, strings.Repeat("a", 10)) {
			t.Errorf("cut at %d: %+v", cut, r)
		}
	}
	if r := Inspect([]byte("abc\xe6\x97"), false); r.Binary == "" {
		t.Error("a file that ends in half a character, and is not cut, is not UTF-8")
	}
}

func TestDirectionControlsAreCountedAndALongLineIsCut(t *testing.T) {
	r := Inspect([]byte("if admin \u202e }{ \u2066ok\u2069\n"), false)
	if r.Bidi != 3 || r.Binary != "" {
		t.Errorf("%+v", r)
	}
	long := bytes.Repeat([]byte("é"), MaxLine) // 2 bytes each: the cut falls inside one
	r = Inspect(append(append(long, '\n'), "short\n"...), false)
	if r.CutLines != 1 || !strings.HasSuffix(r.Text, "\nshort\n") || len(strings.Split(r.Text, "\n")[0]) > MaxLine {
		t.Errorf("cut %d, first line %d bytes", r.CutLines, len(strings.Split(r.Text, "\n")[0]))
	}
	if r := Inspect([]byte("\uFEFFhello"), false); r.Text != "hello" {
		t.Errorf("BOM: %q", r.Text)
	}
}
