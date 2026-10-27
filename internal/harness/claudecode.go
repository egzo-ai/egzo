// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// claudeKey is the placeholder Claude Code is given as its API key. It is not a credential: the
// proxy replaces the x-api-key header on every request to api.anthropic.com. Claude Code needs
// some key to skip its login, and a key it was told to approve to skip the question about using it.
const claudeKey = "sk-ant-api03-egzo-placeholder-0000000000000000000000000000000000000000"

type claudeCode struct{}

func init() { register(claudeCode{}) }

func (claudeCode) Name() string       { return "claude-code" }
func (claudeCode) IdleSignal() string { return "state" }

// The footer under Claude Code's prompt names the permission mode, or offers the shortcuts.
func (claudeCode) ReadyMarkers() []string { return []string{"for shortcuts", "shift+tab to cycle"} }

// claudeOAuthToken is the placeholder for a subscription token; like claudeKey it is replaced by the proxy.
const claudeOAuthToken = "sk-ant-oat01-egzo-placeholder-0000000000000000000000000000000000000000"

func (claudeCode) ContainerEnv(bypass bool, providers Providers) map[string]string {
	// A subscription token comes in CLAUDE_CODE_OAUTH_TOKEN and makes Claude Code send it as a bearer
	// token; an ANTHROPIC_API_KEY next to it would win and be sent as x-api-key.
	env := map[string]string{"ANTHROPIC_API_KEY": claudeKey}
	if providers.AnthropicBearer {
		env = map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": claudeOAuthToken}
	}
	if bypass {
		// Claude Code refuses bypass mode as root unless it is told it runs in a sandbox, which it does.
		env["IS_SANDBOX"] = "1"
	}
	return env
}

// claudeHooks maps each Claude Code hook that reports the agent's life cycle to the state it means.
// This is the one place Claude Code's names are translated into egzo's states. Notification is not here:
// it means idle or blocked by the kind of notification, see claudeNotifications.
var claudeHooks = map[string]string{
	"SessionStart":     "idle",
	"UserPromptSubmit": "working",
	"PreToolUse":       "working",
	"PostToolUse":      "working",
	"Stop":             "idle",
}

// claudeNotifications maps the kinds of Claude Code notification (the hook's matcher) to a state: the
// agent waiting at its prompt is idle, one waiting for a permission or an answer is blocked.
var claudeNotifications = []struct{ matcher, state string }{
	{"idle_prompt", "idle"},
	{"permission_prompt|elicitation_dialog", "blocked"},
}

func stateCommand(state, reason string) string { return "egzo state " + state + " --reason " + reason }

func (c claudeCode) Plan(o Options, args []string) (Plan, error) {
	home := o.Home
	trusted := map[string]any{}
	dirs := append([]string{}, o.Workspaces...)
	if o.Workdir != "" {
		dirs = append(dirs, o.Workdir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		trusted[dir] = map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}
	}

	state := map[string]any{
		"hasCompletedOnboarding": true,
		"customApiKeyResponses":  map[string]any{"approved": []any{claudeKey[len(claudeKey)-20:]}, "rejected": []any{}},
		"projects":               trusted,
		"mcpServers": map[string]any{
			// Claude Code defers an MCP server's tools behind tool search unless told otherwise. egzo's tools stay on the model's own
			// tool list, by the names the instructions use; the browser's many tools are left to tool search, where they take no context.
			"egzo": map[string]any{
				"type":       "http",
				"url":        strings.TrimRight(o.ControlURL, "/") + "/mcp",
				"headers":    map[string]any{"Authorization": o.Authorization},
				"alwaysLoad": true,
			},
		},
	}
	// The browser's server is egzo's to refresh or delete only when egzo wrote it (`egzo agent browser ...`).
	statePath := filepath.Join(home, ".claude.json")
	var replace [][]string
	entry := map[string]any{"type": "stdio", "command": browserCommand[0], "args": toAny(browserCommand[1:]), "env": map[string]any{}}
	if value, present, wholesale := o.browserServer(statePath, []string{"mcpServers"}, entry, ownsClaudeCodeBrowser); present {
		state["mcpServers"].(map[string]any)["playwright"] = value
		if wholesale {
			replace = [][]string{{"mcpServers", "playwright"}}
		}
	}

	hooks := map[string]any{}
	for name, state := range claudeHooks {
		hooks[name] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": stateCommand(state, name)}}}}
	}
	var notifications []any
	for _, n := range claudeNotifications {
		reason := strings.SplitN(n.matcher, "|", 2)[0]
		notifications = append(notifications, map[string]any{"matcher": n.matcher, "hooks": []any{map[string]any{"type": "command", "command": stateCommand(n.state, reason)}}})
	}
	hooks["Notification"] = notifications
	// Bypass mode comes from the command line, not from `permissions.defaultMode` in the settings: with
	// that setting Claude Code asks on its first start "Make auto mode your default permission mode?", a
	// question nobody is there to answer in an unattended agent. The key is removed (null deletes it when
	// merging) so a home an earlier run configured does not keep asking either.
	permissions := map[string]any{"defaultMode": nil}
	settings := map[string]any{
		"hooks":                             hooks,
		"permissions":                       permissions,
		"skipDangerousModePermissionPrompt": o.Bypass,
	}
	if !o.Bypass {
		permissions["defaultMode"] = "default"
	}
	if len(o.Workspaces) > 1 {
		extra := make([]any, 0, len(o.Workspaces))
		for _, dir := range o.Workspaces {
			extra = append(extra, dir)
		}
		permissions["additionalDirectories"] = extra
	}
	if o.Model != "" {
		settings["model"] = o.Model
	}

	instructions := Instructions(o.Prompt)
	command := append([]string{}, args...)
	if len(command) == 0 {
		command = []string{"claude"}
	}
	if o.Bypass {
		command = append(command, "--permission-mode", "bypassPermissions")
	}
	if o.Model != "" {
		command = append(command, "--model", o.Model)
	}
	command = append(command, "--append-system-prompt", string(instructions))

	encode := func(value any) []byte { out, _ := json.MarshalIndent(value, "", "  "); return out }
	return Plan{
		Files: map[string]File{
			statePath: {Content: encode(state), Mode: 0o600, Merge: true, Replace: replace},
			filepath.Join(home, ".claude", "settings.json"): {Content: encode(settings), Mode: 0o644, Merge: true},
			o.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}, nil
}
