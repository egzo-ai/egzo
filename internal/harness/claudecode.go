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
func (claudeCode) IdleSignal() string { return "hook" }

func (claudeCode) ContainerEnv(bypass bool) map[string]string {
	env := map[string]string{"ANTHROPIC_API_KEY": claudeKey}
	if bypass {
		// Claude Code refuses bypass mode as root unless it is told it runs in a sandbox, which it does.
		env["IS_SANDBOX"] = "1"
	}
	return env
}

// claudeHooks are the hooks that report the agent's life cycle to the control sidecar.
var claudeHooks = []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification", "PreToolUse", "PostToolUse"}

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
		"mcpServers": map[string]any{"egzo": map[string]any{
			"type":    "http",
			"url":     strings.TrimRight(o.ControlURL, "/") + "/mcp",
			"headers": map[string]any{"Authorization": o.Authorization},
		}},
	}

	hooks := map[string]any{}
	for _, name := range claudeHooks {
		hooks[name] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "egzo hook " + name}}}}
	}
	permissions := map[string]any{"defaultMode": "default"}
	settings := map[string]any{
		"hooks":                             hooks,
		"permissions":                       permissions,
		"skipDangerousModePermissionPrompt": false,
	}
	if o.Bypass {
		permissions["defaultMode"] = "bypassPermissions"
		settings["skipDangerousModePermissionPrompt"] = true
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
	if o.Model != "" {
		command = append(command, "--model", o.Model)
	}
	command = append(command, "--append-system-prompt", string(instructions))

	encode := func(value any) []byte { out, _ := json.MarshalIndent(value, "", "  "); return out }
	return Plan{
		Files: map[string]File{
			filepath.Join(home, ".claude.json"):             {Content: encode(state), Mode: 0o600, Merge: true},
			filepath.Join(home, ".claude", "settings.json"): {Content: encode(settings), Mode: 0o644, Merge: true},
			o.InstructionsFile:                              {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}, nil
}
