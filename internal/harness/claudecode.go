// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"fmt"
	"os"
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

// The variables `claude auth login` signs in with: the stand-in refresh token of the project's sign-in, and its scope.
// The proxy answers the refresh itself, with the current stand-ins, so the same stand-in works for every agent.
const (
	EnvClaudeRefreshToken = "CLAUDE_CODE_OAUTH_REFRESH_TOKEN"
	EnvClaudeScopes       = "CLAUDE_CODE_OAUTH_SCOPES"
)

func (claudeCode) SignInHost() string { return "api.anthropic.com" }

// SignInEnv carries the sign-in for `claude auth login`, which `egzo agent run` runs when Claude Code has none (Plan).
// Claude Code refuses a refresh token without its scope.
func (claudeCode) SignInEnv(s SignIn) map[string]string {
	if s.Refresh == "" || s.Scope == "" {
		return nil
	}
	return map[string]string{EnvClaudeRefreshToken: s.Refresh, EnvClaudeScopes: s.Scope}
}

// claudeSignedIn reports whether Claude Code keeps a sign-in in home (a Claude subscription's tokens).
func claudeSignedIn(home string) bool {
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return false
	}
	var stored struct {
		ClaudeAiOauth struct {
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	return json.Unmarshal(data, &stored) == nil && stored.ClaudeAiOauth.RefreshToken != ""
}

func (claudeCode) ContainerEnv(bypass bool, providers Providers) map[string]string {
	// A subscription token comes in CLAUDE_CODE_OAUTH_TOKEN and makes Claude Code send it as a bearer
	// token; an ANTHROPIC_API_KEY next to it would win and be sent as x-api-key.
	env := map[string]string{"ANTHROPIC_API_KEY": claudeKey}
	switch {
	case providers.AnthropicSignIn:
		env = map[string]string{}
	case providers.AnthropicBearer:
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
	if o.AHP {
		// the AHP adapter hears of approvals and questions (it answers them from the chat too), of the end of a turn, and of the TUI's start
		for _, name := range claudeAHPHooks {
			hook := map[string]any{"type": "command", "command": claudeAHPHookCommand}
			if name == "PermissionRequest" {
				hook["timeout"] = claudeAHPPermissionTimeout
			}
			if groups, ok := hooks[name].([]any); ok && len(groups) > 0 {
				group := groups[0].(map[string]any)
				group["hooks"] = append(group["hooks"].([]any), hook)
				continue
			}
			hooks[name] = []any{map[string]any{"hooks": []any{hook}}}
		}
	}
	// The permission mode comes from the command line, not from `permissions.defaultMode` in the settings:
	// with that setting (even "default") Claude Code asks on its first start "Make auto mode your default
	// permission mode?", a question nobody is there to answer in an unattended agent. The key is removed
	// (null deletes it when merging) so a home an earlier run configured does not keep asking either.
	permissions := map[string]any{"defaultMode": nil}
	settings := map[string]any{
		"hooks":                             hooks,
		"permissions":                       permissions,
		"skipDangerousModePermissionPrompt": o.Bypass,
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

	instructions := InstructionsFor(o)
	command := append([]string{}, args...)
	if len(command) == 0 {
		command = []string{"claude"}
	}
	if o.Bypass {
		command = append(command, "--permission-mode", "bypassPermissions")
	} else {
		// explicit: Claude Code would otherwise make auto mode its default
		command = append(command, "--permission-mode", "default")
	}
	if o.Model != "" {
		command = append(command, "--model", o.Model)
	}
	command = append(command, "--append-system-prompt", string(instructions))

	// A new agent of a project with a sign-in signs in before its TUI starts, once: the TUI never reads the refresh token.
	var setup [][]string
	if o.env(EnvClaudeRefreshToken) != "" && !claudeSignedIn(home) {
		setup = append(setup, []string{command[0], "auth", "login"})
	}

	encode := func(value any) []byte { out, _ := json.MarshalIndent(value, "", "  "); return out }
	plan := Plan{
		Setup: setup,
		Files: map[string]File{
			statePath: {Content: encode(state), Mode: 0o600, Merge: true, Replace: replace},
			filepath.Join(home, ".claude", "settings.json"): {Content: encode(settings), Mode: 0o644, Merge: true},
			o.InstructionsFile: {Content: instructions},
		},
		Command:      command,
		InterruptKey: "\x1b",
	}
	c.planAHP(o, &plan)
	return plan, nil
}

// AHP mode: Claude Code has no AHP server, and egzo's adapter is one (internal/claudeahp, run as `egzo agent claude-ahp serve` next to
// the TUI). It follows the main session's transcript, hears of approvals and questions through the hooks, and types what the hub sends into
// the TUI: the terminal stays.
func (claudeCode) AHP() AHPMode { return AHPMode{Terminal: true} }

// The adapter's hooks, the command they run, and how long an approval may wait in the chat: Claude Code shows its own dialog meanwhile, and
// the first answer wins.
var claudeAHPHooks = []string{"SessionStart", "PermissionRequest", "Stop", "StopFailure"}

const (
	claudeAHPHookCommand       = "egzo agent claude-ahp hook"
	claudeAHPPermissionTimeout = 86400
	claudeAHPPort              = 8766
)

// ClaudeAHPPath is where the adapter serves AHP, under its address.
const ClaudeAHPPath = "/ahp"

func (c claudeCode) planAHP(o Options, plan *Plan) {
	endpoint := filepath.Join(o.Home, AHPEndpointFile)
	if !o.AHP {
		plan.Remove = append(plan.Remove, endpoint)
		return
	}
	// The main session is named now, and the TUI started on it (--session-id), or continues it after a restart (--resume): Claude Code
	// makes a session when it starts, and egzo shows that one. The name is kept in the home.
	mainFile := filepath.Join(o.Home, ".egzo", "main-session")
	main := ""
	if o.ReadFile != nil {
		if data, err := o.ReadFile(mainFile); err == nil {
			main = strings.TrimSpace(string(data))
		}
	}
	if !validClaudeSession(main) {
		main = newClaudeSession()
		plan.Files[mainFile] = File{Content: []byte(main + "\n"), Mode: 0o644}
	}
	transcript := ClaudeTranscript(o.Home, o.Workdir, main)
	resume := o.Exists != nil && o.Exists(transcript)
	plan.Command = onClaudeSession(plan.Command, main, resume)
	plan.Background = []string{"egzo", "agent", "claude-ahp", "serve", "--transcript", transcript}
	described, _ := json.Marshal(AHPEndpoint{
		URL:         fmt.Sprintf("ws://127.0.0.1:%d%s", claudeAHPPort, ClaudeAHPPath),
		Token:       newAHPToken(),
		MainSession: mainFile,
	})
	plan.Files[endpoint] = File{Content: described, Mode: 0o600}
}

// ClaudeTranscript is where Claude Code keeps a session started in dir: ~/.claude/projects/<dir, every character but letters and digits
// made a dash>/<session>.jsonl.
func ClaudeTranscript(home, dir, session string) string {
	project := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, dir)
	return filepath.Join(home, ".claude", "projects", project, session+".jsonl")
}

// validClaudeSession takes what Claude Code takes as a session id: a UUID.
func validClaudeSession(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}

func newClaudeSession() string {
	b := []byte(newAHPToken()[:32])
	b[12] = '4'
	b[16] = "89ab"[b[16]%4]
	s := string(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// claudeSubcommands are Claude Code's subcommands: a command that runs one is not the TUI.
var claudeSubcommands = map[string]bool{"auth": true, "mcp": true, "plugin": true, "plugins": true, "doctor": true, "update": true,
	"install": true, "migrate-installer": true, "setup-token": true, "config": true}

// onClaudeSession starts Claude Code's TUI on the main session: --session-id the first time, --resume once its transcript is there. A
// command that is not the TUI, or that already says which session (or prints and exits), is left as it is.
func onClaudeSession(command []string, session string, resume bool) []string {
	if len(command) == 0 || filepath.Base(command[0]) != "claude" {
		return command
	}
	for i, arg := range command[1:] {
		name, _, _ := strings.Cut(arg, "=")
		switch name {
		case "-c", "--continue", "-r", "--resume", "--session-id", "-p", "--print", "--fork-session":
			return command
		}
		if i == 0 && claudeSubcommands[arg] {
			return command
		}
	}
	flag := "--session-id"
	if resume {
		flag = "--resume"
	}
	return append([]string{command[0], flag, session}, command[1:]...)
}
