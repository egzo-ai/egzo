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
const readyFile = (process.env.HOME || "/home/agent") + "/.egzo/opencode-ready"
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
    // In AHP mode, the main session egzo named is made now, if it is not there yet, where the agent works: the TUI starts on it (-s), and
    // the chat has it before anybody types. OpenCode itself would make a session for a first prompt only.
    if (ahp && main) {
      const there = await ctx.session.get({ sessionID: main }).catch(() => null)
      if (!there) {
        const location = process.env.EGZO_AHP_DIRECTORY ? { location: { directory: process.env.EGZO_AHP_DIRECTORY } } : {}
        await ctx.session.create({ id: main, ...location }).catch(() => {})
      }
    }
    // It loads once OpenCode's server is up, which can be after the TUI drew its prompt box: egzo types nothing before this file is there.
    try { mkdirSync(dirname(readyFile), { recursive: true }); writeFileSync(readyFile, "ready\n") } catch {}
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
		ReadyFile:    filepath.Join(opts.Home, ".egzo", "opencode-ready"),
	}
	endpoint := filepath.Join(opts.Home, AHPEndpointFile)
	if !opts.AHP || opts.Exists == nil || !opts.Exists(OpenCodeAHPPlugin) {
		// Not in AHP mode, or an image without the plugin (one derived from the stock image that removed it): no AHP server, and
		// `egzo agent ahp` says so. What an earlier run wrote must not point at a server that is not there.
		plan.Remove = append(plan.Remove, endpoint)
		return plan, nil
	}
	token := newAHPToken()
	// The main session is named now, not when OpenCode makes its first session: OpenCode makes one for a first prompt only, and an agent
	// with no task gets none until someone types, so its chat would have nothing to show and nowhere to send the first prompt. The plan's
	// setup makes the session under this name, in the agent's directory, before the TUI starts on it (-s), so the TUI and the chat are the
	// same session from the start (egzo's plugin makes it too if it is still missing). The name is kept in the home: a restart continues it.
	mainFile := filepath.Join(opts.Home, ".egzo", "main-session")
	main := ""
	if opts.ReadFile != nil {
		if data, err := opts.ReadFile(mainFile); err == nil {
			main = strings.TrimSpace(string(data))
		}
	}
	if !validOpenCodeSession(main) {
		main = newOpenCodeSession()
		plan.Files[mainFile] = File{Content: []byte(main + "\n"), Mode: 0o644}
	}
	if command := onSession(plan.Command, main); len(command) != len(plan.Command) {
		plan.Command = command
		// The TUI shows the session it is started on only if the session is there when it starts: one made after stays off its screen
		// until someone types. So the background server the TUI would start is started first, with the same environment (the plugin's
		// settings below), and the main session is made in it; OpenCode takes making a session that is there as a no-op.
		session := map[string]any{"id": main}
		if opts.Workdir != "" {
			session["location"] = map[string]any{"directory": opts.Workdir}
		}
		body, _ := json.Marshal(session)
		plan.Setup = append(plan.Setup,
			[]string{"opencode", "service", "start"},
			[]string{"sh", "-c", `exec opencode api POST /api/session -d "$1" >/dev/null`, "sh", string(body)})
	}
	plan.Env = map[string]string{"OPENCODE_CONFIG_CONTENT": openCodeAHPConfig(token), "EGZO_AHP_DIRECTORY": opts.Workdir}
	described, _ := json.Marshal(AHPEndpoint{
		URL:         fmt.Sprintf("ws://127.0.0.1:%d%s", openCodeAHPPort, openCodeAHPPath),
		Token:       token,
		MainSession: mainFile,
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

// validOpenCodeSession takes what OpenCode takes as a session id (Session.ID: it starts with "ses"), in the characters egzo writes and
// `egzo agent ahp` reads back.
func validOpenCodeSession(id string) bool {
	if !strings.HasPrefix(id, "ses") || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// newOpenCodeSession names a main session: OpenCode's own ids are "ses_" and 26 characters; egzo's say they are egzo's.
func newOpenCodeSession() string {
	return "ses_egzo" + newAHPToken()[:22]
}

// openCodeSubcommands are OpenCode's subcommands: a command that runs one is not the TUI, and is left as it is.
var openCodeSubcommands = map[string]bool{
	"upgrade": true, "update": true, "uninstall": true, "acp": true, "api": true, "debug": true, "auth": true, "mcp": true, "plugin": true,
	"models": true, "stats": true, "mini": true, "run": true, "session": true, "service": true, "serve": true, "web": true, "export": true,
	"import": true, "github": true, "agent": true,
}

// onSession starts OpenCode's TUI on a session: `opencode -s ID`, the rest of the command after it. A command that is not OpenCode's TUI,
// or that already says which session (-s, --session, -c, --continue), is left as it is.
func onSession(command []string, session string) []string {
	if len(command) == 0 || filepath.Base(command[0]) != "opencode" {
		return command
	}
	for _, arg := range command[1:] {
		name, _, _ := strings.Cut(arg, "=")
		if name == "-s" || name == "--session" || name == "-c" || name == "--continue" || openCodeSubcommands[arg] {
			return command
		}
	}
	return append([]string{command[0], "-s", session}, command[1:]...)
}
