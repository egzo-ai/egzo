package termsafe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLineIsOneHarmlessLine(t *testing.T) {
	cases := map[string]string{
		"plain":                           "plain",
		"two  words\tand\nlines":          "two words and lines",
		"\x1b[2Jclear":                    "[2Jclear",
		"title\x1b]0;evil\x07 end":        "title ]0;evil end",
		"c1\u009bx":                       "c1 x",
		"  padded  ":                      "padded",
		"nul\x00byte":                     "nul byte",
		"unicode é 你好":                    "unicode é 你好",
		"forged\np-fake-1  fake  running": "forged p-fake-1 fake running",
		"del\x7fchar":                     "del char",
	}
	for in, want := range cases {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockKeepsBreaksAndDropsControls(t *testing.T) {
	got := Block("a\tb\r\nc\x1b[31mred\x1b[0m\x07\u009d")
	if strings.ContainsAny(got, "\x1b\x07\u009d") || !strings.Contains(got, "a\tb") || strings.Count(got, "\n") != 2 {
		t.Errorf("Block = %q", got)
	}
}

func TestTruncateCutsOnRuneBoundaries(t *testing.T) {
	got := Truncate(strings.Repeat("é", 100), 10)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 10 || !strings.HasSuffix(got, "...") {
		t.Errorf("Truncate = %q", got)
	}
	if Truncate("short", 10) != "short" {
		t.Error("a short text was changed")
	}
}
