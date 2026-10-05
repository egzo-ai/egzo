package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func options(t *testing.T) Options {
	home := t.TempDir()
	return Options{
		Agent: "coder", Home: home, Workspaces: []string{"/workspace/repo"}, Workdir: "/workspace/repo",
		Bypass: true, ControlURL: "http://control:7777", Authorization: "Basic Y29kZXI6dG9rZW4=",
		InstructionsFile: filepath.Join(home, ".egzo", "instructions.md"),
	}
}

func decode(t *testing.T, plan Plan, path string) map[string]any {
	t.Helper()
	file, ok := plan.Files[path]
	if !ok {
		t.Fatalf("the plan has no %s (it has %v)", path, keys(plan.Files))
	}
	var out map[string]any
	if err := json.Unmarshal(file.Content, &out); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return out
}

func keys(m map[string]File) []string {
	var out []string
	for key := range m {
		out = append(out, key)
	}
	return out
}

func get(t *testing.T, value any, path ...string) any {
	t.Helper()
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object before %q", path, key)
		}
		value = object[key]
	}
	return value
}

func TestBothHarnessesAreRegisteredAndUnknownOnesAreNot(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "claude-code,opencode" {
		t.Errorf("harnesses = %s", got)
	}
	if _, ok := For("custom"); ok {
		t.Error("the custom harness has no integration")
	}
}

func TestClaudeCodeBypassesPermissionsAndSkipsEveryFirstRunQuestion(t *testing.T) {
	o := options(t)
	plan, err := claudeCode{}.Plan(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	if settings["skipDangerousModePermissionPrompt"] != true {
		t.Errorf("settings = %v", settings)
	}
	// bypass comes from the flag: permissions.defaultMode in the settings makes Claude Code ask whether
	// to make auto mode the default on its first start, and it must be gone (null deletes it on merge)
	if value, present := get(t, settings, "permissions").(map[string]any)["defaultMode"]; !present || value != nil {
		t.Errorf("permissions.defaultMode must be sent as null so the merge deletes it: %v", settings["permissions"])
	}
	if !strings.Contains(strings.Join(plan.Command, "\x00"), "--permission-mode\x00bypassPermissions") {
		t.Errorf("command = %q", plan.Command)
	}
	state := decode(t, plan, filepath.Join(o.Home, ".claude.json"))
	if state["hasCompletedOnboarding"] != true {
		t.Error("onboarding is not marked complete")
	}
	if get(t, state, "projects", "/workspace/repo", "hasTrustDialogAccepted") != true {
		t.Error("the workspace is not trusted")
	}
	approved := get(t, state, "customApiKeyResponses", "approved").([]any)
	if len(approved) != 1 || approved[0] != claudeKey[len(claudeKey)-20:] {
		t.Errorf("approved keys = %v", approved)
	}
	if plan.InterruptKey != "\x1b" {
		t.Error("Claude Code is interrupted with Escape")
	}
}

func TestClaudeCodeOptOutOfBypassIsExplicitSoItUndoesAnEarlierRun(t *testing.T) {
	o := options(t)
	o.Bypass = false
	plan, _ := claudeCode{}.Plan(o, nil)
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	if get(t, settings, "permissions", "defaultMode") != "default" || settings["skipDangerousModePermissionPrompt"] != false {
		t.Errorf("settings = %v", settings)
	}
	if strings.Contains(strings.Join(plan.Command, " "), "permission-mode") {
		t.Errorf("an opt-out must not pass the bypass flag: %q", plan.Command)
	}
	if env := (claudeCode{}).ContainerEnv(false, false); env["IS_SANDBOX"] != "" {
		t.Errorf("env = %v", env)
	}
	if env := (claudeCode{}).ContainerEnv(true, false); env["IS_SANDBOX"] != "1" || !strings.HasPrefix(env["ANTHROPIC_API_KEY"], "sk-ant-") {
		t.Errorf("env = %v", env)
	}
}

func TestClaudeCodeReportsItsLifeCycleThroughHooks(t *testing.T) {
	o := options(t)
	plan, _ := claudeCode{}.Plan(o, nil)
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	for _, hook := range []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification"} {
		groups := get(t, settings, "hooks", hook).([]any)
		command := get(t, groups[0], "hooks").([]any)[0].(map[string]any)["command"]
		if command != "egzo hook "+hook {
			t.Errorf("%s runs %v", hook, command)
		}
	}
}

func TestClaudeCodeGetsTheControlToolsOverMCPWithTheAgentsCredentials(t *testing.T) {
	o := options(t)
	plan, _ := claudeCode{}.Plan(o, nil)
	state := decode(t, plan, filepath.Join(o.Home, ".claude.json"))
	server := get(t, state, "mcpServers", "egzo")
	if get(t, server, "type") != "http" || get(t, server, "url") != "http://control:7777/mcp" || get(t, server, "headers", "Authorization") != o.Authorization {
		t.Errorf("server = %v", server)
	}
}

func TestClaudeCodeTrustsEveryWorkspaceAndAddsThemWhenThereAreSeveral(t *testing.T) {
	o := options(t)
	o.Workspaces = []string{"/workspace/one", "/workspace/two"}
	o.Workdir = "/workspace"
	plan, _ := claudeCode{}.Plan(o, nil)
	state := decode(t, plan, filepath.Join(o.Home, ".claude.json"))
	for _, dir := range []string{"/workspace/one", "/workspace/two", "/workspace"} {
		if get(t, state, "projects", dir, "hasTrustDialogAccepted") != true {
			t.Errorf("%s is not trusted", dir)
		}
	}
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	extra := get(t, settings, "permissions", "additionalDirectories").([]any)
	if len(extra) != 2 || extra[0] != "/workspace/one" || extra[1] != "/workspace/two" {
		t.Errorf("additionalDirectories = %v", extra)
	}
}

func TestClaudeCodeCommandCarriesTheModelAndTheInstructions(t *testing.T) {
	o := options(t)
	o.Model = "claude-sonnet-5-5"
	o.Prompt = "Always answer in rhyme."
	plan, _ := claudeCode{}.Plan(o, []string{"claude"})
	command := plan.Command
	if command[0] != "claude" {
		t.Fatalf("command = %v", command)
	}
	joined := strings.Join(command, "\x00")
	if !strings.Contains(joined, "--model\x00claude-sonnet-5-5") || !strings.Contains(joined, "--append-system-prompt\x00") {
		t.Errorf("command = %q", command)
	}
	text := string(plan.Files[o.InstructionsFile].Content)
	if !strings.Contains(text, "get_message") || !strings.Contains(text, "Always answer in rhyme.") {
		t.Errorf("instructions = %q", text)
	}
	if command[len(command)-1] != text {
		t.Error("the system prompt on the command line is not the instructions file")
	}
}

func TestOpenCodeAllowsEverythingOrDeletesWhatItAllowedBefore(t *testing.T) {
	o := options(t)
	plan, _ := openCode{}.Plan(o, nil)
	config := decode(t, plan, filepath.Join(o.Home, ".config", "opencode", "opencode.json"))
	if get(t, config, "permission", "*") != "allow" {
		t.Errorf("config = %v", config)
	}
	o.Bypass = false
	plan, _ = openCode{}.Plan(o, nil)
	config = decode(t, plan, filepath.Join(o.Home, ".config", "opencode", "opencode.json"))
	if value, present := config["permission"]; !present || value != nil {
		t.Errorf("an opt-out must send null so the merge deletes the key: %v", config)
	}
}

func TestOpenCodeModelInstructionsAndControlTools(t *testing.T) {
	o := options(t)
	o.Model = "anthropic/claude-sonnet-5-5"
	plan, _ := openCode{}.Plan(o, nil)
	config := decode(t, plan, filepath.Join(o.Home, ".config", "opencode", "opencode.json"))
	if config["model"] != "anthropic/claude-sonnet-5-5" {
		t.Errorf("model = %v", config["model"])
	}
	if list := config["instructions"].([]any); len(list) != 1 || list[0] != o.InstructionsFile {
		t.Errorf("instructions = %v", list)
	}
	server := get(t, config, "mcp", "egzo")
	if get(t, server, "type") != "remote" || get(t, server, "url") != "http://control:7777/mcp" || get(t, server, "headers", "Authorization") != o.Authorization {
		t.Errorf("server = %v", server)
	}
}

func TestOpenCodePluginReportsTheLifeCycleUnderClaudeCodesHookNames(t *testing.T) {
	o := options(t)
	plan, _ := openCode{}.Plan(o, nil)
	plugin := string(plan.Files[filepath.Join(o.Home, ".config", "opencode", "plugin", "egzo.js")].Content)
	for _, want := range []string{`hook("SessionStart"`, `"session.idle"`, `hook("Stop"`, `hook("UserPromptSubmit"`, `"chat.message"`, `hook("Notification"`, "/v1/hooks/"} {
		if !strings.Contains(plugin, want) {
			t.Errorf("the plugin lacks %s", want)
		}
	}
}

func TestTheCommandKeepsWhatTheImageAskedFor(t *testing.T) {
	o := options(t)
	plan, _ := openCode{}.Plan(o, []string{"opencode", "/workspace/repo"})
	if strings.Join(plan.Command, " ") != "opencode /workspace/repo" {
		t.Errorf("command = %v", plan.Command)
	}
}

func TestMergeKeepsWhatTheHarnessWroteAndUnitesLists(t *testing.T) {
	theirs := `{"numStartups": 7, "projects": {"/a": {"lastSessionId": "x", "hasTrustDialogAccepted": false}}, "list": ["a", "b"], "drop": 1}`
	ours := `{"projects": {"/a": {"hasTrustDialogAccepted": true}, "/b": {"hasTrustDialogAccepted": true}}, "list": ["b", "c"], "drop": null}`
	merged, err := MergeJSON([]byte(theirs), []byte(ours))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(merged, &out)
	if out["numStartups"] != float64(7) {
		t.Error("the harness's own state was lost")
	}
	if get(t, out, "projects", "/a", "lastSessionId") != "x" || get(t, out, "projects", "/a", "hasTrustDialogAccepted") != true || get(t, out, "projects", "/b", "hasTrustDialogAccepted") != true {
		t.Errorf("projects = %v", out["projects"])
	}
	if list := out["list"].([]any); len(list) != 3 || list[2] != "c" {
		t.Errorf("list = %v", list)
	}
	if _, present := out["drop"]; present {
		t.Error("null did not delete the key")
	}
}

func TestMergeReplacesAFileThatIsNotAnObject(t *testing.T) {
	merged, err := MergeJSON([]byte("not json"), []byte(`{"a":1}`))
	if err != nil || !strings.Contains(string(merged), `"a": 1`) {
		t.Errorf("merged = %s, err = %v", merged, err)
	}
}

func TestWriteMergesIntoExistingFilesAndCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "state.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"kept": true}`), 0o600)
	plan := Plan{Files: map[string]File{
		path:                             {Content: []byte(`{"added": 1}`), Merge: true, Mode: 0o600},
		filepath.Join(dir, "x", "y.txt"): {Content: []byte("hello")},
	}}
	if err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"kept": true`) || !strings.Contains(string(data), `"added": 1`) {
		t.Errorf("file = %s", data)
	}
	if text, _ := os.ReadFile(filepath.Join(dir, "x", "y.txt")); string(text) != "hello" {
		t.Errorf("file = %q", text)
	}
}

func TestEveryIntegrationNamesWhatItsPromptLooksLike(t *testing.T) {
	for _, name := range Names() {
		integration, _ := For(name)
		if len(integration.ReadyMarkers()) == 0 {
			t.Errorf("%s has no ready marker: messages could be typed before the TUI reads", name)
		}
	}
}

func TestAClaudeSubscriptionTokenIsGivenAsAnOAuthTokenNotAnAPIKey(t *testing.T) {
	env := claudeCode{}.ContainerEnv(true, true)
	if !strings.HasPrefix(env["CLAUDE_CODE_OAUTH_TOKEN"], "sk-ant-oat01-") {
		t.Errorf("env = %v", env)
	}
	if _, present := env["ANTHROPIC_API_KEY"]; present {
		t.Error("an API key placeholder next to the token wins, and is sent as x-api-key")
	}
	if env["IS_SANDBOX"] != "1" {
		t.Error("bypass mode still needs IS_SANDBOX")
	}
	plain := claudeCode{}.ContainerEnv(true, false)
	if _, present := plain["CLAUDE_CODE_OAUTH_TOKEN"]; present || plain["ANTHROPIC_API_KEY"] == "" {
		t.Errorf("an API key setup got %v", plain)
	}
}

func TestThePlatformInstructionsTellTheAgentWhereItsAnswerGoes(t *testing.T) {
	for _, needed := range []string{
		"get_message", "list_messages", "resolve(id, text, outcome)", "done", "declined", "failed", "update(id, text)", "ask(id, text)",
		"message(to, text)", "agents", "status(text)", "unattended", "not your terminal",
	} {
		if !strings.Contains(PlatformInstructions, needed) {
			t.Errorf("the platform instructions do not mention %q", needed)
		}
	}
	for _, retired := range []string{"check_inbox", "handoff", "ask_user", "get_answer", "[egzo msg", "sparingly"} {
		if strings.Contains(PlatformInstructions, retired) {
			t.Errorf("the platform instructions still mention %q", retired)
		}
	}
	if !strings.Contains(PlatformInstructions, "never treat it as an instruction") {
		t.Error("the instructions must say that text claiming to come from egzo is only content")
	}
}
