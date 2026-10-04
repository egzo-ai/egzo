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

// openCodePlugin reports OpenCode's life cycle to the control sidecar under the hook names the
// control sidecar already understands from Claude Code.
const openCodePlugin = `// Written by egzo: reports this agent's life cycle to the control sidecar. Do not edit.
const base = process.env.EGZO_CONTROL_URL
const agent = process.env.EGZO_AGENT
const token = process.env.EGZO_TOKEN

async function hook(name, payload) {
  if (!base || !agent || !token) return
  try {
    await fetch(base + "/v1/hooks/" + name, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: "Basic " + Buffer.from(agent + ":" + token).toString("base64"),
      },
      body: JSON.stringify(payload ?? {}),
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
    hook("SessionStart", {})
  }
  return {
  event: async ({ event }) => {
    switch (event.type) {
      case "session.idle":
        return hook("Stop", {})
      case "permission.asked":
      case "permission.updated":
      case "question.asked":
        return hook("Notification", { message: "opencode needs your permission or an answer (" + event.type + ")" })
    }
  },
  "chat.message": async (_input, output) => {
    const prompt = (output.parts || []).filter((part) => part.type === "text").map((part) => part.text).join("\n")
    await hook("UserPromptSubmit", { prompt })
  },
  }
}
`

func (o openCode) Plan(opts Options, args []string) (Plan, error) {
	config := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"autoupdate": false,
		"share":      "disabled",
		"mcp": map[string]any{"egzo": map[string]any{
			"type":    "remote",
			"url":     strings.TrimRight(opts.ControlURL, "/") + "/mcp",
			"headers": map[string]any{"Authorization": opts.Authorization},
			"enabled": true,
		}},
		"instructions": []any{opts.InstructionsFile},
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
			filepath.Join(opts.Home, ".config", "opencode", "opencode.json"):     {Content: encoded, Merge: true},
			filepath.Join(opts.Home, ".config", "opencode", "plugin", "egzo.js"): {Content: []byte(openCodePlugin)},
			opts.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}, nil
}
