// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discordbridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAShortTextIsOnePart(t *testing.T) {
	if got := split("hello", 100); len(got) != 1 || got[0] != "hello" {
		t.Errorf("%q", got)
	}
}

func TestALongTextIsCutAtAParagraphALineOrAWordAndEveryPartFits(t *testing.T) {
	text := strings.Repeat("word ", 50) + "\n\n" + strings.Repeat("line\n", 40) + strings.Repeat("x", 300)
	parts := split(text, 120)
	if len(parts) < 3 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 120 {
			t.Errorf("part %d is %d characters", i, n)
		}
	}
	if joined := strings.Join(parts, ""); strings.Count(joined, "word") != 50 || strings.Count(joined, "line") != 40 || strings.Count(joined, "x") != 300 {
		t.Error("something was lost")
	}
	if strings.HasSuffix(parts[0], " wo") {
		t.Errorf("cut in a word: %q", parts[0])
	}
}

func TestACodeBlockCutInTwoIsClosedAndOpenedAgainWithItsLanguage(t *testing.T) {
	text := "Here:\n```go\n" + strings.Repeat("fmt.Println(1)\n", 30) + "```\nDone."
	parts := split(text, 200)
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d leaves a block open: %q", i, p)
		}
		if i > 0 && i < len(parts)-1 && !strings.HasPrefix(p, "```go\n") {
			t.Errorf("part %d does not open the block again: %q", i, p)
		}
	}
}

func TestAQuestionIsAContainerWithItsChoicesAsButtonsAndPingsOnlyThePerson(t *testing.T) {
	msgs := render(Event{Agent: "coder-1", Kind: "question", ID: "m1", Text: "staging or production? @everyone", Choices: []string{"staging", "production"}}, "77")
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	if m.Flags != 1<<15 || len(m.AllowedMentions.Parse) != 0 || len(m.AllowedMentions.Users) != 1 || m.AllowedMentions.Users[0] != "77" {
		t.Errorf("flags %d mentions %+v", m.Flags, m.AllowedMentions)
	}
	body := string(m.Components)
	for _, want := range []string{`"type":17`, `"custom_id":"egzo:c:0"`, `"custom_id":"egzo:c:1"`, `"label":"production"`, "<@77>", "Reply to this message"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in %s", want, body)
		}
	}
	var parsed []any
	if json.Unmarshal(m.Components, &parsed) != nil {
		t.Error("components are not JSON")
	}
}

func TestATextTooLongForAFewMessagesGoesAsAFile(t *testing.T) {
	text := strings.Repeat("a long paragraph of an answer. ", 600)
	msgs := render(Event{Agent: "coder-1", Kind: "resolution", Outcome: "done", ID: "m9", Text: text}, "77")
	if len(msgs) != 1 || len(msgs[0].Files) != 1 || string(msgs[0].Files[0].Data) != strings.TrimSpace(text) {
		t.Fatalf("%d messages, files %v", len(msgs), len(msgs[0].Files))
	}
	if !strings.Contains(string(msgs[0].Components), "attachment://coder-1-m9.md") {
		t.Error("the file is not shown")
	}
	medium := render(Event{Agent: "coder-1", Kind: "update", ID: "m8", Text: strings.Repeat("word ", 1500)}, "77")
	if len(medium) < 2 || len(medium[1].AllowedMentions.Users) != 0 {
		t.Errorf("a text of a few parts: %d messages, the second pings %v", len(medium), medium[1].AllowedMentions.Users)
	}
}
