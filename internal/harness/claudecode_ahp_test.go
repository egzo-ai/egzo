// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func claudeAHPOptions(t *testing.T) Options {
	o := options(t)
	o.AHP = true
	o.ReadFile = os.ReadFile
	o.Exists = func(path string) bool { _, err := os.Stat(path); return err == nil }
	return o
}

func TestClaudeCodeHasAnAHPModeThatKeepsItsTerminal(t *testing.T) {
	mode, ok := AHPModeOf("claude-code")
	if !ok || !mode.Terminal {
		t.Errorf("claude-code: %v %v", mode, ok)
	}
	if got := WithAHP(); !reflect.DeepEqual(got, []string{"claude-code", "opencode"}) {
		t.Errorf("harnesses with an AHP mode: %v", got)
	}
}

// Claude Code makes a session when its TUI starts: egzo names it, so the adapter knows the main session before Claude Code writes a line,
// starts the TUI on it, and continues it after a restart.
func TestClaudeCodeInAHPModeStartsItsTUIOnAMainSessionItNames(t *testing.T) {
	o := claudeAHPOptions(t)
	mainFile := filepath.Join(o.Home, ".egzo", "main-session")
	plan, err := claudeCode{}.Plan(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	written, ok := plan.Files[mainFile]
	if !ok {
		t.Fatalf("the main session is named before the harness starts: %v", keys(plan.Files))
	}
	main := strings.TrimSpace(string(written.Content))
	if !validClaudeSession(main) {
		t.Fatalf("main session %q is not a UUID", main)
	}
	if len(plan.Command) < 3 || !reflect.DeepEqual(plan.Command[:3], []string{"claude", "--session-id", main}) {
		t.Errorf("command = %v: the TUI starts on the main session", plan.Command)
	}
	transcript := ClaudeTranscript(o.Home, "/workspace/repo", main)
	if transcript != filepath.Join(o.Home, ".claude", "projects", "-workspace-repo", main+".jsonl") {
		t.Errorf("transcript = %s", transcript)
	}
	if !reflect.DeepEqual(plan.Background, []string{"egzo", "agent", "claude-ahp", "serve", "--transcript", transcript}) {
		t.Errorf("background = %v", plan.Background)
	}
	if plan.Prompt != nil {
		t.Errorf("lines are typed into the TUI, which the transcript mirrors: prompt = %v", plan.Prompt)
	}

	var endpoint AHPEndpoint
	file := plan.Files[filepath.Join(o.Home, AHPEndpointFile)]
	if err := json.Unmarshal(file.Content, &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.URL != "ws://127.0.0.1:8766/ahp" || len(endpoint.Token) < 32 || endpoint.MainSession != mainFile || file.Mode != 0o600 {
		t.Errorf("endpoint = %+v (mode %o)", endpoint, file.Mode)
	}

	// a restart keeps the session, and resumes it once Claude Code wrote it
	if err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(transcript), 0o755)
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	again, _ := claudeCode{}.Plan(o, nil)
	if _, rewritten := again.Files[mainFile]; rewritten {
		t.Error("a main session already named is kept")
	}
	if !reflect.DeepEqual(again.Command[:3], []string{"claude", "--resume", main}) {
		t.Errorf("after a restart, command = %v", again.Command)
	}
	var second AHPEndpoint
	_ = json.Unmarshal(again.Files[filepath.Join(o.Home, AHPEndpointFile)].Content, &second)
	if second.Token == endpoint.Token {
		t.Error("each start has a token of its own")
	}
}

func TestClaudeCodeInAHPModeTellsTheAdapterOfApprovalsAndTurns(t *testing.T) {
	o := claudeAHPOptions(t)
	plan, _ := claudeCode{}.Plan(o, nil)
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	hooks := settings["hooks"].(map[string]any)
	commands := func(event string) []string {
		var out []string
		for _, group := range hooks[event].([]any) {
			for _, h := range group.(map[string]any)["hooks"].([]any) {
				out = append(out, h.(map[string]any)["command"].(string))
			}
		}
		return out
	}
	for _, event := range []string{"SessionStart", "PermissionRequest", "Stop", "StopFailure"} {
		if !contains(commands(event), "egzo agent claude-ahp hook") {
			t.Errorf("%s: %v", event, commands(event))
		}
	}
	// egzo's own state hooks stay
	if !contains(commands("Stop"), "egzo state idle --reason Stop") || !contains(commands("SessionStart"), "egzo state idle --reason SessionStart") {
		t.Errorf("state hooks: %v %v", commands("Stop"), commands("SessionStart"))
	}
	// an approval waits in the chat as long as in the TUI
	permission := hooks["PermissionRequest"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if permission["timeout"] != float64(86400) {
		t.Errorf("PermissionRequest hook = %v", permission)
	}
}

func TestClaudeCodeOutOfAHPModeHasNoAdapter(t *testing.T) {
	o := options(t)
	plan, _ := claudeCode{}.Plan(o, nil)
	if plan.Background != nil || strings.Contains(strings.Join(plan.Command, " "), "--session-id") {
		t.Errorf("plan = %v %v", plan.Background, plan.Command)
	}
	if !contains(plan.Remove, filepath.Join(o.Home, AHPEndpointFile)) {
		t.Error("what an earlier run described is removed")
	}
	settings := decode(t, plan, filepath.Join(o.Home, ".claude", "settings.json"))
	if strings.Contains(string(plan.Files[filepath.Join(o.Home, ".claude", "settings.json")].Content), "claude-ahp") || settings["hooks"].(map[string]any)["PermissionRequest"] != nil {
		t.Error("no adapter hooks")
	}
}

func TestClaudeCodeInAHPModeLeavesAChosenSessionOrAnotherCommandAlone(t *testing.T) {
	for _, command := range [][]string{
		{"claude", "--continue"}, {"claude", "--resume", "abc"}, {"claude", "-p", "hello"}, {"claude", "--session-id=x"},
		{"claude", "mcp", "list"}, {"bash"},
	} {
		got := onClaudeSession(command, "11111111-2222-4333-8444-555555555555", false)
		if !reflect.DeepEqual(got, command) {
			t.Errorf("%v became %v", command, got)
		}
	}
	got := onClaudeSession([]string{"/usr/local/bin/claude", "--model", "opus"}, "11111111-2222-4333-8444-555555555555", false)
	if !reflect.DeepEqual(got, []string{"/usr/local/bin/claude", "--session-id", "11111111-2222-4333-8444-555555555555", "--model", "opus"}) {
		t.Errorf("got %v", got)
	}
}

func TestAClaudeSessionIDIsAUUID(t *testing.T) {
	for i := 0; i < 50; i++ {
		if id := newClaudeSession(); !validClaudeSession(id) {
			t.Fatalf("%q", id)
		}
	}
	for _, bad := range []string{"", "ses_abc", "../../etc/passwd", "11111111-2222-4333-8444-55555555555Z", "11111111x2222-4333-8444-555555555555"} {
		if validClaudeSession(bad) {
			t.Errorf("%q", bad)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
