// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package golden reads and rewrites the sections of a golden Markdown file: under a "## " heading, the first ````text block holds
// the exact expected text. Tests compare what egzo produces with it word for word, and rewrite it when run with -update.
package golden

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const open, closing = "````text\n", "````"

// Check compares got with the block under heading in the file at path, or writes got there when update is set.
func Check(t *testing.T, path, heading, got string, update bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start, end, err := block(doc, heading)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	got = strings.TrimSuffix(got, "\n")
	if update {
		content := got
		if content != "" {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(doc[:start]+content+doc[end:]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := strings.TrimSuffix(doc[start:end], "\n"); got != want {
		t.Errorf("%s, %q differs from the golden file (rerun with -update if the change is deliberate)\n--- want\n%s\n--- got\n%s", path, heading, want, got)
	}
}

// block finds the content of the first text block under heading: from just after its opening fence to its closing fence.
func block(doc, heading string) (int, int, error) {
	at := strings.Index(doc, "\n## "+heading+"\n")
	if at < 0 {
		return 0, 0, fmt.Errorf("no heading %q", heading)
	}
	rel := strings.Index(doc[at:], open)
	if rel < 0 {
		return 0, 0, fmt.Errorf("no text block under %q", heading)
	}
	start := at + rel + len(open)
	end := strings.Index(doc[start:], closing)
	if end < 0 {
		return 0, 0, fmt.Errorf("the text block under %q is not closed", heading)
	}
	if end > 0 && doc[start+end-1] != '\n' {
		return 0, 0, fmt.Errorf("the closing fence under %q is not on its own line", heading)
	}
	return start, start + end, nil
}
