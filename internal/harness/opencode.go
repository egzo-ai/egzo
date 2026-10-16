// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type openCode struct{}

func init() { register(openCode{}) }

func (openCode) Name() string       { return "opencode" }
func (openCode) IdleSignal() string { return "hook" }

// OpenCode's prompt box says "Ask anything…" until the first message.
func (openCode) ReadyMarkers() []string { return []string{"Ask anything"} }

func (openCode) ContainerEnv(bool, bool) map[string]string {
	// The proxy replaces the credential of every request to api.anthropic.com.
	return map[string]string{"ANTHROPIC_API_KEY": claudeKey}
}

// openCodePlugin reports OpenCode's life cycle to the control sidecar as egzo's states.
const openCodePlugin = `// Written by egzo: reports this agent's state to the control sidecar. Do not edit.
const base = process.env.EGZO_CONTROL_URL
const agent = process.env.EGZO_AGENT
const token = process.env.EGZO_TOKEN

async function state(name, report) {
  if (!base || !agent || !token) return
  try {
    await fetch(base + "/v1/state", {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: "Basic " + Buffer.from(agent + ":" + token).toString("base64"),
      },
      body: JSON.stringify({ state: name, ...report }),
      signal: AbortSignal.timeout(2000),
    })
  } catch {}
}

let started = false

export const EgzoPlugin = async () => {
  // OpenCode only creates a session when the first prompt is sent, so the plugin loading is the
  // moment the TUI is up and waiting.
  if (!started) {
    started = true
    state("idle", { reason: "started" })
  }
  return {
  event: async ({ event }) => {
    switch (event.type) {
      case "session.idle":
        return state("idle", { reason: event.type })
      case "permission.asked":
      case "permission.updated":
        return state("blocked", { reason: "permission" })
      case "question.asked":
        return state("blocked", { reason: "question" })
    }
  },
  "chat.message": async (_input, output) => {
    const prompt = (output.parts || []).filter((part) => part.type === "text").map((part) => part.text).join("\n")
    await state("working", { reason: "prompt", prompt })
  },
  }
}
`

func (o openCode) Plan(opts Options, args []string) (Plan, error) {
	config := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"autoupdate": false,
		"share":      "disabled",
		"mcp": map[string]any{
			"egzo": map[string]any{
				"type":    "remote",
				"url":     strings.TrimRight(opts.ControlURL, "/") + "/mcp",
				"headers": map[string]any{"Authorization": opts.Authorization},
				"enabled": true,
			},
		},
		"instructions": []any{opts.InstructionsFile},
	}
	// The browser's server is egzo's to refresh or delete only when egzo wrote it (a command starting with `egzo agent browser`).
	configPath := filepath.Join(opts.Home, ".config", "opencode", "opencode.json")
	var replace [][]string
	entry := map[string]any{"type": "local", "command": toAny(browserCommand), "enabled": true}
	if value, present, wholesale := opts.browserServer(configPath, []string{"mcp"}, entry, ownsOpenCodeBrowser); present {
		config["mcp"].(map[string]any)["playwright"] = value
		if wholesale {
			replace = [][]string{{"mcp", "playwright"}}
		}
	}
	if opts.Bypass {
		config["permission"] = map[string]any{"*": "allow"}
	} else {
		config["permission"] = nil // delete: what an earlier run allowed must not outlive the opt-out
	}
	if opts.Model != "" {
		config["model"] = opts.Model
	}
	command := append([]string{}, args...)
	if len(command) == 0 {
		command = []string{"opencode"}
	}
	encoded, _ := json.MarshalIndent(config, "", "  ")
	instructions := Instructions(opts.Prompt)
	return Plan{
		Files: map[string]File{
			configPath: {Content: encoded, Merge: true, Mode: 0o600, Replace: replace},
			filepath.Join(opts.Home, ".config", "opencode", "plugin", "egzo.js"): {Content: []byte(openCodePlugin)},
			opts.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}, nil
}
