// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type openCode struct{}

func init() { register(openCode{}) }

func (openCode) Name() string       { return "opencode" }
func (openCode) IdleSignal() string { return "state" }

// OpenCode's prompt box says "Ask anything…" until the first message.
func (openCode) ReadyMarkers() []string { return []string{"Ask anything"} }

// openCodeKey is the placeholder OpenCode is given as its OpenCode Go or Console key. It is not a credential either.
const openCodeKey = "oc_s_egzo-placeholder-0000000000000000000000000000000000"

func (openCode) ContainerEnv(bool, Providers) map[string]string {
	// The proxy replaces the credential of every request to api.anthropic.com and to opencode.ai.
	return map[string]string{"ANTHROPIC_API_KEY": claudeKey, "OPENCODE_API_KEY": openCodeKey}
}

// openCodePlugin reports OpenCode's life cycle to the control sidecar as egzo's states. It is an OpenCode 2 plugin: a default
// export with a setup function, a hook on the prompt and the server's event stream.
const openCodePlugin = `// Written by egzo: reports this agent's state to the control sidecar. Do not edit.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs"
import { dirname } from "node:path"

const base = process.env.EGZO_CONTROL_URL
const agent = process.env.EGZO_AGENT
const token = process.env.EGZO_TOKEN
// In AHP mode egzo shows one session of the agent, its main session: the first one OpenCode creates. Its id is kept in a file of the home,
// and only its life cycle is the agent's state; sessions opened after it still work, and egzo does not count them.
const ahp = process.env.EGZO_AHP === "1"
const mainFile = (process.env.HOME || "/home/agent") + "/.egzo/main-session"
let main = null
if (ahp) {
  try { main = readFileSync(mainFile, "utf8").trim() || null } catch {}
}
function sessionOf(event) {
  return event?.data?.sessionID ?? event?.properties?.sessionID ?? event?.properties?.info?.id ?? event?.sessionID
}
function created(event) {
  if (!ahp || main || event.type !== "session.created") return
  const id = sessionOf(event)
  if (!id || event?.data?.parentID || event?.properties?.info?.parentID) return
  main = id
  try { mkdirSync(dirname(mainFile), { recursive: true }); writeFileSync(mainFile, id + "\n") } catch {}
}
// mine says whether an event is about the agent's state: every event, unless egzo knows a main session and the event is about another.
function mine(event) {
  if (!ahp || !main) return true
  const id = sessionOf(event)
  return !id || id === main
}

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
    await ctx.session.hook("prompt", (event) => mine(event) && state("working", { reason: "prompt", prompt: event.prompt?.text ?? "" }))
    const stop = new AbortController()
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: stop.signal })) {
          created(event)
          if (!mine(event)) continue
          switch (event.type) {
            // A turn that ends is session.execution.*: OpenCode 2 declares session.idle and session.status but does not send them
            // (anomalyco/opencode#55358); they are handled for the day it does.
            case "session.execution.succeeded":
            case "session.execution.failed":
            case "session.execution.interrupted":
            case "session.idle":
              await state("idle", { reason: event.type })
              break
            case "session.status":
              if ((event.properties?.status?.type ?? event.properties?.status) === "idle") await state("idle", { reason: event.type })
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
	// OpenCode puts an MCP server's tools behind Code Mode's `execute` unless told otherwise. egzo's tools stay on the model's own
	// tool list, by the names the instructions use; the browser's many tools are left to Code Mode, where they take no context.
	servers := map[string]any{
		"egzo": map[string]any{
			"type":     "remote",
			"url":      strings.TrimRight(opts.ControlURL, "/") + "/mcp",
			"headers":  map[string]any{"Authorization": opts.Authorization},
			"codemode": false,
		},
	}
	config := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"update":       "disable",
		"share":        "disabled",
		"instructions": []any{opts.InstructionsFile},
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
	config["mcp"] = map[string]any{"servers": servers}
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
	instructions := InstructionsFor(opts)
	plan := Plan{
		Files: map[string]File{
			configPath: {Content: encoded, Merge: true, Mode: 0o600, Replace: replace},
			filepath.Join(opts.Home, ".config", "opencode", "plugins", "egzo.js"): {Content: []byte(openCodePlugin)},
			opts.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}
	endpoint := filepath.Join(opts.Home, AHPEndpointFile)
	if !opts.AHP || opts.Exists == nil || !opts.Exists(OpenCodeAHPPlugin) {
		// Not in AHP mode, or an image without the plugin (one derived from the stock image that removed it): no AHP server, and
		// `egzo agent ahp` says so. What an earlier run wrote must not point at a server that is not there.
		plan.Remove = append(plan.Remove, endpoint)
		return plan, nil
	}
	token := newAHPToken()
	plan.Env = map[string]string{"OPENCODE_CONFIG_CONTENT": openCodeAHPConfig(token)}
	described, _ := json.Marshal(AHPEndpoint{
		URL:         fmt.Sprintf("ws://127.0.0.1:%d%s", openCodeAHPPort, openCodeAHPPath),
		Token:       token,
		MainSession: filepath.Join(opts.Home, ".egzo", "main-session"),
	})
	plan.Files[endpoint] = File{Content: described, Mode: 0o600}
	return plan, nil
}

// OpenCode serves AHP through egzo's fork of the opencode-ahp plugin (https://github.com/egzo-ai/opencode-ahp), which the stock image
// installs here. It runs inside OpenCode's server, the background service the TUI starts, so it sees the TUI's own sessions.
const OpenCodeAHPPlugin = "/usr/local/lib/node_modules/opencode-ahp"

const (
	openCodeAHPPort = 8765
	openCodeAHPPath = "/ahp"
)

// AHP: the plugin leaves the TUI where it is.
func (openCode) AHP() AHPMode { return AHPMode{Terminal: true} }

// openCodeAHPConfig is what OpenCode is given in OPENCODE_CONFIG_CONTENT, which it merges over its config files, and which the background
// service inherits from the TUI that starts it: the user's opencode.json is left alone. The plugin listens on the loopback only, asks for
// the token, serves no terminal and no changeset (the hub shows the chat), and the project's location is never unloaded for being idle,
// which would stop the listener (an AHP connection is not activity to OpenCode).
func openCodeAHPConfig(token string) string {
	config := map[string]any{
		"experimental": map[string]any{"location_idle_timeout": false},
		"plugins": []any{map[string]any{
			"package": OpenCodeAHPPlugin,
			"options": map[string]any{
				"host": "127.0.0.1", "port": openCodeAHPPort, "path": openCodeAHPPath, "token": token,
				"capabilities": map[string]any{"terminals": false, "changesets": false, "automations": false},
			},
		}},
	}
	encoded, _ := json.Marshal(config)
	return string(encoded)
}
