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
func (openCode) IdleSignal() string { return "state" }

// OpenCode's prompt box says "Ask anything…" until the first message.
func (openCode) ReadyMarkers() []string { return []string{"Ask anything"} }

func (openCode) ContainerEnv(bool, bool) map[string]string {
	// The proxy replaces the credential of every request to api.anthropic.com.
	return map[string]string{"ANTHROPIC_API_KEY": claudeKey}
}

// openCodePlugin reports OpenCode's life cycle to the control sidecar as egzo's states. It is an OpenCode 2 plugin: a default
// export with a setup function, a hook on the prompt and the server's event stream.
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

export default {
  id: "egzo",
  async setup(ctx) {
    // The plugin loading is the moment the TUI is up and waiting: OpenCode only creates a session when the first prompt is sent.
    state("idle", { reason: "started" })
    await ctx.session.hook("prompt", (event) => state("working", { reason: "prompt", prompt: event.prompt?.text ?? "" }))
    const stop = new AbortController()
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: stop.signal })) {
          switch (event.type) {
            case "session.idle":
            case "session.execution.failed":
            case "session.execution.interrupted":
              await state("idle", { reason: event.type })
              break
            case "permission.asked":
              await state("blocked", { reason: "permission" })
              break
            case "form.created":
              await state("blocked", { reason: "question" })
              break
            case "permission.replied":
            case "form.replied":
            case "form.cancelled":
              await state("working", { reason: event.type })
              break
          }
        }
      } catch {}
    })()
    return () => stop.abort()
  },
}
`

func (o openCode) Plan(opts Options, args []string) (Plan, error) {
	servers := map[string]any{
		"egzo": map[string]any{
			"type":    "remote",
			"url":     strings.TrimRight(opts.ControlURL, "/") + "/mcp",
			"headers": map[string]any{"Authorization": opts.Authorization},
		},
	}
	config := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"update":       "disable",
		"share":        "disabled",
		"instructions": []any{opts.InstructionsFile},
		// What OpenCode 1 versions of egzo wrote under their old names: the home outlives the agent, and OpenCode 2 would
		// read both.
		"autoupdate": nil,
		"permission": nil,
	}
	// The browser's server is egzo's to refresh or delete only when egzo wrote it (a command starting with `egzo agent browser`).
	configPath := filepath.Join(opts.Home, ".config", "opencode", "opencode.json")
	var replace [][]string
	entry := map[string]any{"type": "local", "command": toAny(browserCommand)}
	if value, present, wholesale := opts.browserServer(configPath, []string{"mcp", "servers"}, entry, ownsOpenCodeBrowser); present {
		servers["playwright"] = value
		if wholesale {
			replace = [][]string{{"mcp", "servers", "playwright"}}
		}
	}
	// The old place of the servers, before OpenCode 2 (mcp.<name>): egzo's own entries there are deleted.
	legacy := map[string]any{"servers": servers, "egzo": nil}
	if old, exists := existingServer(configPath, []string{"mcp"}); exists && ownsOpenCodeBrowser(old) {
		legacy["playwright"] = nil
	}
	config["mcp"] = legacy
	if opts.Bypass {
		config["permissions"] = []any{map[string]any{"action": "*", "resource": "*", "effect": "allow"}}
	} else {
		config["permissions"] = nil // delete: what an earlier run allowed must not outlive the opt-out
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
			filepath.Join(opts.Home, ".config", "opencode", "plugins", "egzo.js"): {Content: []byte(openCodePlugin)},
			opts.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}, nil
}
