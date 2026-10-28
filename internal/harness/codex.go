// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type codex struct{}

func init() { register(codex{}) }

func (codex) Name() string       { return "codex" }
func (codex) IdleSignal() string { return "state" }

// Codex's composer says "To get started, describe a task" until the first message, and "Ask Codex to do anything" after.
func (codex) ReadyMarkers() []string {
	return []string{"describe a task", "Ask Codex to do anything"}
}

// codexKey is the placeholder Codex is given as its OpenAI API key, when the agent's profile binds the `openai` service:
// the proxy replaces it on every request to api.openai.com. It is not a credential.
const codexKey = "sk-proj-egzo-placeholder-000000000000000000000000000000000000000000"

// EnvCodexAuth carries the auth.json of a ChatGPT sign-in into a new agent: the stand-ins of the project's sign-in,
// which work only through the proxy. Spawn sets it when the project has one.
const EnvCodexAuth = "EGZO_CODEX_AUTH"

// SignInHost is any: Codex signs in wherever its profile's OAuth service is (the built-in `chatgpt`, or one of the user's).
func (codex) SignInHost() string { return "" }

// SignInEnv carries Codex's auth.json, which `egzo agent run` writes when there is none (Plan).
func (codex) SignInEnv(s SignIn) map[string]string {
	return map[string]string{EnvCodexAuth: CodexAuth(s.Access, s.Refresh, s.ID, s.Refreshed.UTC().Format(time.RFC3339))}
}

func (codex) ContainerEnv(_ bool, providers Providers) map[string]string {
	if providers.OpenAIKey {
		return map[string]string{"OPENAI_API_KEY": codexKey}
	}
	return map[string]string{}
}

// codexHooks are the hooks that report Codex's life cycle as egzo's states. Codex reads a hook's output: a Stop hook
// that exits 0 must print JSON, so every one prints an empty object. SessionStart runs at the first prompt, not when the
// TUI comes up: the session holder reports the first idle once the TUI is ready.
var codexHooks = []struct{ event, state string }{
	{"SessionStart", "idle"},
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"PostToolUse", "working"},
	{"PermissionRequest", "blocked"},
	{"Stop", "idle"},
}

// Plan starts Codex with egzo's settings as `-c` overrides on its command line. Nothing is written into the user's
// config.toml, which lives in the agent's home and keeps what the user put there; a setting removed from egzo is gone
// at the next start. Hooks given this way are not ones a person reviewed, so Codex runs them only with
// --dangerously-bypass-hook-trust: egzo wrote them, and they only report the agent's state.
func (c codex) Plan(o Options, args []string) (Plan, error) {
	overrides := []string{
		"check_for_update_on_startup=false",
		"features.use_agent_identity=false", // an identity Codex would register and sign with is a credential the agent holds
		"analytics.enabled=false",
		"mcp_servers.egzo.url=" + tomlString(strings.TrimRight(o.ControlURL, "/")+"/mcp"),
		"mcp_servers.egzo.http_headers={Authorization=" + tomlString(o.Authorization) + "}",
		"sandbox_mode=" + tomlString("danger-full-access"), // the container is the sandbox
		"developer_instructions=" + tomlString(string(Instructions(o.Prompt))),
	}
	if o.Bypass {
		overrides = append(overrides, "approval_policy="+tomlString("never"))
	} else {
		overrides = append(overrides, "approval_policy="+tomlString("on-request"))
	}
	if o.Model != "" {
		overrides = append(overrides, "model="+tomlString(o.Model))
	}
	dirs := append([]string{}, o.Workspaces...)
	if o.Workdir != "" {
		dirs = append(dirs, o.Workdir)
	}
	sort.Strings(dirs)
	for _, dir := range slices.Compact(dirs) {
		overrides = append(overrides, "projects."+tomlString(dir)+".trust_level="+tomlString("trusted"))
	}
	for _, hook := range codexHooks {
		command := stateCommand(hook.state, hook.event) + " >/dev/null 2>&1; echo '{}'"
		overrides = append(overrides, fmt.Sprintf("hooks.%s=[{hooks=[{type=%s,command=%s,timeout=10}]}]", hook.event, tomlString("command"), tomlString(command)))
	}
	if o.hasBrowser() {
		overrides = append(overrides, "mcp_servers.playwright.command="+tomlString(browserCommand[0]),
			"mcp_servers.playwright.args=["+strings.Join(tomlStrings(browserCommand[1:]), ",")+"]")
	}

	command := append([]string{}, args...)
	if len(command) == 0 {
		command = []string{"codex"}
	}
	if filepath.Base(command[0]) == "codex" {
		flags := []string{"--dangerously-bypass-hook-trust"}
		for _, override := range overrides {
			flags = append(flags, "-c", override)
		}
		command = append(append([]string{command[0]}, flags...), command[1:]...)
	}

	files := map[string]File{}
	// auth.json is Codex's: it rewrites it at every refresh. egzo writes one only when there is none, with stand-ins
	// for a ChatGPT sign-in the project has, or the placeholder key of the `openai` service.
	authPath := filepath.Join(o.Home, ".codex", "auth.json")
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		switch {
		case o.env(EnvCodexAuth) != "":
			files[authPath] = File{Content: []byte(o.env(EnvCodexAuth)), Mode: 0o600}
		case o.env("OPENAI_API_KEY") == codexKey:
			auth, _ := json.Marshal(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": codexKey})
			files[authPath] = File{Content: auth, Mode: 0o600}
		}
	}
	return Plan{
		Files:        files,
		Command:      command,
		InterruptKey: "\x1b",
		Dirs:         []string{filepath.Join(o.Home, ".codex")},
	}, nil
}

// tomlString writes s as a TOML basic string: quotes and backslashes escaped, control characters as \uXXXX.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlStrings(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = tomlString(s)
	}
	return out
}

// CodexAuth is the auth.json of a ChatGPT sign-in, made of the stand-ins of the project's sign-in.
func CodexAuth(access, refresh, id, lastRefresh string) string {
	data, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens":         map[string]any{"id_token": id, "access_token": access, "refresh_token": refresh},
		"last_refresh":   lastRefresh,
	})
	return string(data)
}
