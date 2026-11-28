// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"encoding/json"
	"strings"
)

// Claude Code keeps each session in a transcript: a JSON Lines file under ~/.claude/projects, one entry per line, appended as the session
// goes. It is the adapter's source for the chat. What is read of it is listed here; the rest of an entry (and every other kind of entry)
// is left alone, so a new field or a new kind of entry changes nothing.
//
//   - a person's prompt: a "user" entry whose content is text (typed into the TUI, or pasted by egzo);
//   - the agent's answer: "assistant" entries, one content block each (text, thinking, tool_use), several per API message, with the
//     message's id and usage;
//   - a tool's result: a "user" entry with tool_result blocks, and the tool's own record of it (toolUseResult);
//   - an interrupt: a "user" entry that says "[Request interrupted by user]" (or "... for tool use");
//   - steering: "queue-operation" entries when a message typed during a turn is queued, and taken into the turn (absorbed_mid_turn);
//   - the end of a turn: a "system" entry turn_duration, with how long it took;
//   - the session's title: "ai-title" (Claude Code's own) and "custom-title" (/rename).
type entry struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	UUID              string          `json:"uuid"`
	Timestamp         string          `json:"timestamp"`
	SessionID         string          `json:"sessionId"`
	IsSidechain       bool            `json:"isSidechain"`
	IsMeta            bool            `json:"isMeta"`
	IsCompactSummary  bool            `json:"isCompactSummary"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	DurationMs        int64           `json:"durationMs"`
	Message           *message        `json:"message"`
	ToolUseResult     json.RawMessage `json:"toolUseResult"`
	// queue-operation
	Operation string `json:"operation"`
	Content   string `json:"content"`
	Reason    string `json:"reason"`
	// titles
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
}

type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *apiUsage       `json:"usage"`
}

type apiUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// block is one content block of a message.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func parseEntry(line []byte) (entry, bool) {
	var e entry
	if json.Unmarshal(line, &e) != nil || e.Type == "" {
		return entry{}, false
	}
	return e, true
}

// blocks reads a message's content: a string is one text block.
func (m *message) blocks() []block {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []block{{Type: "text", Text: text}}
	}
	var blocks []block
	_ = json.Unmarshal(m.Content, &blocks)
	return blocks
}

// Interrupt markers: the "user" entry Claude Code writes when Esc stops a turn.
const (
	interrupted        = "[Request interrupted by user]"
	interruptedForTool = "[Request interrupted by user for tool use]"
)

// userKind says what a "user" entry is.
type userKind int

const (
	userOther     userKind = iota // meta, a command's output, a compaction summary: not part of the chat
	userPrompt                    // a person's prompt: it starts a turn
	userResults                   // tool results
	userInterrupt                 // Esc
)

// classifyUser reads a "user" entry: its kind, the prompt's text, the tool results it carries.
func classifyUser(e entry) (userKind, string, []block) {
	if e.IsMeta || e.IsCompactSummary || e.Message == nil {
		return userOther, "", nil
	}
	blocks := e.Message.blocks()
	var results []block
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			results = append(results, b)
		case "text":
			texts = append(texts, b.Text)
		}
	}
	if len(results) > 0 {
		return userResults, "", results
	}
	text := strings.Join(texts, "\n")
	trimmed := strings.TrimSpace(text)
	switch {
	case trimmed == "":
		return userOther, "", nil
	case trimmed == interrupted || trimmed == interruptedForTool:
		return userInterrupt, "", nil
	case strings.HasPrefix(trimmed, "<command-"), strings.HasPrefix(trimmed, "<local-command-"), strings.HasPrefix(trimmed, "<bash-"),
		strings.HasPrefix(trimmed, "<system-reminder>"), strings.HasPrefix(trimmed, "<task-notification>"):
		// slash commands and their output, `!` shell commands, what Claude Code adds on its own: the TUI's business, not the chat's
		return userOther, "", nil
	}
	return userPrompt, text, nil
}

// resultText is the text of a tool_result's content: a string, or text blocks (an image says so).
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, p.Text)
		case "image":
			out = append(out, "[image]")
		}
	}
	return strings.Join(out, "\n")
}
