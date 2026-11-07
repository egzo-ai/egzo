// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discordbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egzo-ai/egzo/hub/internal/discord"
)

// What an agent says, as Discord shows it: a Components V2 message, a container whose colour says what it is, the agent's name, the text,
// and a question's choices as buttons. A text longer than one message is split, at a paragraph, a line or a word, and a code block cut in
// two is closed and opened again; one too long for a few messages is attached as a file instead.

const (
	// partText is the most text one message carries: Discord allows 4000 characters of text in a Components V2 message, and the
	// header and the footer take some.
	partText = 3500
	// maxParts is how many messages a text is split into before it goes as a file.
	maxParts = 4
	// maxButtons is how many choices become buttons: five rows of five.
	maxButtons = 25
)

// Component types of Components V2.
const (
	componentRow       = 1
	componentButton    = 2
	componentText      = 10
	componentFile      = 13
	componentSeparator = 14
	componentContainer = 17
	buttonPrimary      = 1
	buttonSecondary    = 2
)

// look is how a kind of message is shown: its colour and its first words.
func look(m Event, mention string) (int, string) {
	who := "**" + m.Agent + "**"
	switch m.Kind {
	case "question":
		return 0x5865F2, fmt.Sprintf("❓ %s asks %s", who, mention)
	case "update":
		return 0xFEE75C, fmt.Sprintf("⏳ %s, for %s", who, mention)
	case "resolution":
		switch m.Outcome {
		case "declined":
			return 0x99AAB5, fmt.Sprintf("🚫 %s declined, %s", who, mention)
		case "failed":
			return 0xED4245, fmt.Sprintf("⚠️ %s could not do it, %s", who, mention)
		}
		return 0x57F287, fmt.Sprintf("✅ %s is done, %s", who, mention)
	}
	return 0x5865F2, fmt.Sprintf("📨 %s asks %s", who, mention)
}

// render turns what an agent says to a person into the messages to post. account is the person's Discord id: they are the only one
// the first message may ping.
func render(m Event, account discord.Snowflake) []discord.MessageCreate {
	mention := "<@" + string(account) + ">"
	color, header := look(m, mention)
	text := strings.TrimSpace(m.Text)
	parts := split(text, partText)
	var file []discord.File
	if len(parts) > maxParts {
		parts = []string{strings.TrimSpace(parts[0]) + "\n\n*… the whole text is attached.*"}
		file = []discord.File{{Name: m.Agent + "-" + m.ID + ".md", Data: []byte(text)}}
	}
	var out []discord.MessageCreate
	for i, part := range parts {
		var inner []any
		if i == 0 {
			inner = append(inner, textDisplay(header))
		} else {
			inner = append(inner, textDisplay(fmt.Sprintf("-# %s, %d/%d", m.Agent, i+1, len(parts))))
		}
		inner = append(inner, textDisplay(part))
		last := i == len(parts)-1
		if last && file != nil {
			inner = append(inner, map[string]any{"type": componentFile, "file": map[string]string{"url": "attachment://" + file[0].Name}})
		}
		if last && m.Kind == "question" && len(m.Choices) > 0 {
			inner = append(inner, buttons(m.Choices)...)
		}
		if last && (m.Kind == "question" || m.Kind == "request") {
			inner = append(inner, map[string]any{"type": componentSeparator, "divider": false}, textDisplay("-# Reply to this message to answer "+m.Agent+"."))
		}
		components := rawJSON([]any{map[string]any{"type": componentContainer, "accent_color": color, "components": inner}})
		mentions := &discord.AllowedMentions{Parse: []string{}}
		if i == 0 {
			mentions.Users = []discord.Snowflake{account}
		}
		msg := discord.MessageCreate{Components: components, Flags: discord.MessageFlagComponentsV2, AllowedMentions: mentions}
		if last {
			msg.Files = file
		}
		out = append(out, msg)
	}
	return out
}

// rawJSON is v as JSON, with <, > and & as they are: a mention is <@id>.
func rawJSON(v any) json.RawMessage {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return json.RawMessage(strings.TrimSpace(b.String()))
}

func textDisplay(content string) map[string]any {
	return map[string]any{"type": componentText, "content": content}
}

// buttons are a question's choices, five to a row. A button's id is its place in the choices: the post it is on says which question.
func buttons(choices []string) []any {
	var rows []any
	var row []any
	for i, c := range choices {
		if i == maxButtons {
			break
		}
		style := buttonSecondary
		if i == 0 {
			style = buttonPrimary
		}
		row = append(row, map[string]any{"type": componentButton, "style": style, "label": truncate(c, 80), "custom_id": fmt.Sprintf("egzo:c:%d", i)})
		if len(row) == 5 {
			rows = append(rows, map[string]any{"type": componentRow, "components": row})
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, map[string]any{"type": componentRow, "components": row})
	}
	return rows
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// split cuts a text into parts of at most max characters: at a paragraph if it can, else a line, else a word, else anywhere. A code block
// cut in two is closed at the end of a part and opened again, with its language, at the start of the next.
func split(text string, max int) []string {
	var parts []string
	fence := "" // the fence line of a code block left open by the last part
	for {
		r := []rune(text)
		room := max
		if fence != "" {
			room -= len([]rune(fence)) + 1
		}
		if len(r) <= room {
			parts = append(parts, prefix(fence, text))
			return parts
		}
		cut := best(string(r[:room-4])) // room for a closing fence
		part, rest := string(r[:cut]), strings.TrimLeft(string(r[cut:]), "\n ")
		part = prefix(fence, part)
		if open := openFence(part); open != "" {
			parts = append(parts, strings.TrimRight(part, "\n")+"\n```")
			fence = open
		} else {
			parts = append(parts, strings.TrimRight(part, "\n "))
			fence = ""
		}
		text = rest
	}
}

func prefix(fence, s string) string {
	if fence == "" {
		return s
	}
	return fence + "\n" + s
}

// best is where to cut a piece of text, in runes: after its last paragraph break, line break or space, or at its end.
func best(piece string) int {
	r := []rune(piece)
	min := len(r) / 2 // never a part that is mostly empty
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(piece, sep); i >= 0 {
			at := len([]rune(piece[:i])) + len([]rune(sep))
			if at > min {
				return at
			}
		}
	}
	return len(r)
}

// openFence is the fence line of a code block a text leaves open, or "".
func openFence(text string) string {
	open := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if open == "" {
				open = trimmed
			} else {
				open = ""
			}
		}
	}
	return open
}
