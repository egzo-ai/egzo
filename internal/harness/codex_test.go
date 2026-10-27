// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// overrides are the `-c` values of a Codex command line.
func overrides(command []string) []string {
	var out []string
	for i, arg := range command {
		if arg == "-c" && i+1 < len(command) {
			out = append(out, command[i+1])
		}
	}
	return out
}

func TestCodexRunsWithEgzosSettingsOnItsCommandLineAndWritesNoConfig(t *testing.T) {
	o := options(t)
	plan, err := codex{}.Plan(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Command[0] != "codex" || !slices.Contains(plan.Command, "--dangerously-bypass-hook-trust") {
		t.Fatalf("command = %q", plan.Command)
	}
	set := overrides(plan.Command)
	for _, want := range []string{
		`approval_policy="never"`, `sandbox_mode="danger-full-access"`, "features.use_agent_identity=false", "check_for_update_on_startup=false",
		`mcp_servers.egzo.url="http://control:7777/mcp"`, `mcp_servers.egzo.http_headers={Authorization="Basic Y29kZXI6dG9rZW4="}`,
		`projects."/workspace/repo".trust_level="trusted"`,
	} {
		if !slices.Contains(set, want) {
			t.Errorf("missing -c %s in %q", want, set)
		}
	}
	var hooks []string
	for _, value := range set {
		if strings.HasPrefix(value, "hooks.") {
			hooks = append(hooks, value)
		}
	}
	if len(hooks) != len(codexHooks) || !strings.Contains(strings.Join(hooks, "\n"), `hooks.Stop=[{hooks=[{type="command",command="egzo state idle --reason Stop >/dev/null 2>&1; echo '{}'",timeout=10}]}]`) {
		t.Errorf("hooks = %q", hooks)
	}
	for path := range plan.Files {
		if strings.HasSuffix(path, "config.toml") {
			t.Error("egzo writes the user's config.toml")
		}
	}
	if def := (codex{}); slices.Contains(overrides(must(def.Plan(Options{Bypass: false}, nil)).Command), `approval_policy="never"`) {
		t.Error("permissions: default still never asks")
	}
}

func must(plan Plan, err error) Plan {
	if err != nil {
		panic(err)
	}
	return plan
}

func TestCodexGetsAnAuthFileOnlyWhenItHasNone(t *testing.T) {
	o := options(t)
	env := map[string]string{"OPENAI_API_KEY": codexKey}
	o.Getenv = func(key string) string { return env[key] }
	auth := filepath.Join(o.Home, ".codex", "auth.json")
	plan, _ := codex{}.Plan(o, nil)
	if got := string(plan.Files[auth].Content); got != `{"OPENAI_API_KEY":"`+codexKey+`","auth_mode":"apikey"}` || plan.Files[auth].Mode != 0o600 {
		t.Errorf("api key auth.json = %s", got)
	}
	env[EnvCodexAuth] = CodexAuth("a", "r", "i", "2026-10-27T00:00:00Z")
	plan, _ = codex{}.Plan(o, nil)
	if got := string(plan.Files[auth].Content); !strings.Contains(got, `"auth_mode":"chatgpt"`) || !strings.Contains(got, `"refresh_token":"r"`) {
		t.Errorf("sign-in auth.json = %s", got)
	}
	os.MkdirAll(filepath.Dir(auth), 0o755)
	os.WriteFile(auth, []byte("codex's own"), 0o600)
	if plan, _ = (codex{}).Plan(o, nil); len(plan.Files) != 0 {
		t.Error("egzo replaced the auth.json Codex keeps")
	}
	if env := (codex{}).ContainerEnv(true, Providers{}); len(env) != 0 {
		t.Errorf("without the openai service: %v", env)
	}
}

func TestTOMLStringsEscapeWhatTOMLNeeds(t *testing.T) {
	if got := tomlString("a\"b\\c\nd"); got != `"a\"b\\c\u000Ad"` {
		t.Errorf("tomlString = %s", got)
	}
}
