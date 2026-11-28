// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// How a Claude Code tool call reads in the chat: its name for people, what it is about to do, and what it did. A tool the adapter does not
// know reads by its own name.
type toolWords struct {
	display, invocation, past string
}

func describeTool(name string, input json.RawMessage) toolWords {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(key string) string {
		s, _ := in[key].(string)
		return s
	}
	code := func(s string) string { return "`" + clip(oneLine(s), 120) + "`" }
	switch name {
	case "Bash":
		return toolWords{"Shell", "Run " + code(str("command")), "Ran " + code(str("command"))}
	case "Read":
		return toolWords{"Read file", "Read " + code(str("file_path")), "Read " + code(str("file_path"))}
	case "Write":
		return toolWords{"Write file", "Write " + code(str("file_path")), "Wrote " + code(str("file_path"))}
	case "Edit", "MultiEdit":
		return toolWords{"Edit file", "Edit " + code(str("file_path")), "Edited " + code(str("file_path"))}
	case "NotebookEdit":
		return toolWords{"Edit notebook", "Edit " + code(str("notebook_path")), "Edited " + code(str("notebook_path"))}
	case "Glob":
		return toolWords{"Find files", "Find " + code(str("pattern")), "Found " + code(str("pattern"))}
	case "Grep":
		return toolWords{"Search", "Search for " + code(str("pattern")), "Searched for " + code(str("pattern"))}
	case "WebFetch":
		return toolWords{"Fetch", "Fetch " + str("url"), "Fetched " + str("url")}
	case "WebSearch":
		return toolWords{"Web search", "Search the web for " + code(str("query")), "Searched the web for " + code(str("query"))}
	case "Task", "Agent":
		d := str("description")
		return toolWords{"Subagent", "Start a subagent: " + d, "Ran a subagent: " + d}
	case "TodoWrite":
		return toolWords{"Todo list", "Update the todo list", "Updated the todo list"}
	case "AskUserQuestion":
		return toolWords{"Question", "Ask a question", "Asked a question"}
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		server, tool, _ := strings.Cut(rest, "__")
		display := server + ": " + tool
		return toolWords{display, display, display}
	}
	return toolWords{name, name, name}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return s
}

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// maxResult is the longest tool result the chat keeps, in bytes: a tool can print megabytes, and every snapshot carries them.
const maxResult = 16 << 10

func clipResult(s string) string {
	if len(s) <= maxResult {
		return s
	}
	cut := maxResult
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more bytes)", len(s)-cut)
}

// Questions: Claude Code's AskUserQuestion takes up to a few questions, each with a header, options (a label and a description) and
// multiSelect; the TUI also offers to type an answer. The answer goes back as the tool's input, with answers keyed by the question's text,
// several labels of one question joined by ", ".
type askInput struct {
	Questions []askQuestion `json:"questions"`
}

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	MultiSelect bool        `json:"multiSelect"`
	Options     []askOption `json:"options"`
}

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// inputRequest is AskUserQuestion's input as an AHP input request: question i is "q<i>", its option j "q<i>o<j>".
func inputRequest(id string, input json.RawMessage) (map[string]any, askInput, bool) {
	var ask askInput
	if json.Unmarshal(input, &ask) != nil || len(ask.Questions) == 0 {
		return nil, ask, false
	}
	questions := []any{}
	for i, q := range ask.Questions {
		options := []any{}
		for j, o := range q.Options {
			option := map[string]any{"id": fmt.Sprintf("q%do%d", i, j), "label": o.Label}
			if o.Description != "" {
				option["description"] = o.Description
			}
			options = append(options, option)
		}
		kind := "single-select"
		if q.MultiSelect {
			kind = "multi-select"
		}
		question := map[string]any{"id": fmt.Sprintf("q%d", i), "kind": kind, "message": q.Question, "required": true, "options": options,
			"allowFreeformInput": true}
		if q.Header != "" {
			question["title"] = q.Header
		}
		questions = append(questions, question)
	}
	return map[string]any{"id": id, "message": "Claude Code asks", "questions": questions}, ask, true
}

// answersFor turns the answers of an AHP chat/inputCompleted into AskUserQuestion's: by question text, labels (or what was typed).
func answersFor(ask askInput, answers map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for i, q := range ask.Questions {
		raw, ok := answers[fmt.Sprintf("q%d", i)]
		if !ok {
			continue
		}
		var a struct {
			State string `json:"state"`
			Value struct {
				Kind           string          `json:"kind"`
				Value          json.RawMessage `json:"value"`
				FreeformValues []string        `json:"freeformValues"`
			} `json:"value"`
			FreeformValues []string `json:"freeformValues"`
		}
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		label := func(id string) string {
			for j, o := range q.Options {
				if id == fmt.Sprintf("q%do%d", i, j) {
					return o.Label
				}
			}
			return id
		}
		var picked []string
		switch a.Value.Kind {
		case "selected":
			var id string
			if json.Unmarshal(a.Value.Value, &id) == nil && id != "" {
				picked = append(picked, label(id))
			}
		case "selected-many":
			var ids []string
			_ = json.Unmarshal(a.Value.Value, &ids)
			for _, id := range ids {
				picked = append(picked, label(id))
			}
		case "text":
			var s string
			if json.Unmarshal(a.Value.Value, &s) == nil && s != "" {
				picked = append(picked, s)
			}
		default:
			var v any
			if json.Unmarshal(a.Value.Value, &v) == nil && v != nil {
				picked = append(picked, fmt.Sprint(v))
			}
		}
		picked = append(picked, a.Value.FreeformValues...)
		picked = append(picked, a.FreeformValues...)
		if len(picked) > 0 {
			out[q.Question] = strings.Join(picked, ", ")
		}
	}
	return out
}

// answersOf reads the answers the TUI gave (AskUserQuestion's toolUseResult) as AHP answers, for the chat to show what was chosen.
func answersOf(ask askInput, result json.RawMessage) map[string]any {
	var r struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal(result, &r) != nil || len(r.Answers) == 0 {
		return nil
	}
	out := map[string]any{}
	for i, q := range ask.Questions {
		given, ok := r.Answers[q.Question]
		if !ok {
			continue
		}
		var ids, free []string
		for _, part := range strings.Split(given, ", ") {
			found := false
			for j, o := range q.Options {
				if o.Label == part {
					ids, found = append(ids, fmt.Sprintf("q%do%d", i, j)), true
					break
				}
			}
			if !found {
				free = append(free, part)
			}
		}
		value := map[string]any{"kind": "selected-many", "value": ids}
		if !q.MultiSelect {
			value = map[string]any{"kind": "selected", "value": ""}
			if len(ids) > 0 {
				value["value"] = ids[0]
			}
		}
		if len(free) > 0 {
			value["freeformValues"] = free
		}
		out[fmt.Sprintf("q%d", i)] = map[string]any{"state": "submitted", "value": value}
	}
	return out
}
