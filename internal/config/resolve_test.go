// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// resolve parses and resolves a project file in dir, the way the CLI does.
func resolve(t *testing.T, dir, yaml string) (*Resolved, []string, error) {
	t.Helper()
	file, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Resolve(file, "proj", dir)
}

func mustResolve(t *testing.T, dir, yaml string) (*Resolved, []string) {
	t.Helper()
	resolved, warnings, err := resolve(t, dir, yaml)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved, warnings
}

// wantProblems asserts that resolution fails and that every fragment appears in the report.
func wantProblems(t *testing.T, dir, yaml string, fragments ...string) {
	t.Helper()
	_, _, err := resolve(t, dir, yaml)
	if err == nil {
		t.Fatalf("Resolve accepted the file, want problems %q", fragments)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not contain %q", err, fragment)
		}
	}
}

func TestResolveAppliesDefaults(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
egress:
  default: { allow: [api.anthropic.com] }
agents:
  coder: { harness: claude-code }
`)
	agent := resolved.Agents["coder"]
	if agent.Permissions != "bypass" {
		t.Errorf("permissions = %q, want bypass", agent.Permissions)
	}
	if agent.Egress != "default" {
		t.Errorf("egress = %q, want default", agent.Egress)
	}
	if agent.Workdir != "/workspace" {
		t.Errorf("workdir = %q, want /workspace", agent.Workdir)
	}
	if len(agent.Workspaces) != 0 {
		t.Errorf("workspaces = %v, want none", agent.Workspaces)
	}
	if resolved.Name != "proj" {
		t.Errorf("name = %q", resolved.Name)
	}
}

func TestResolveEmptyProjectDeniesEverything(t *testing.T) {
	resolved, warnings := mustResolve(t, t.TempDir(), "")
	profile, ok := resolved.Egress["default"]
	if !ok {
		t.Fatal("no default profile")
	}
	if len(profile.Allow) != 0 || len(profile.Services) != 0 {
		t.Errorf("default profile = %+v, want empty", profile)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestResolveVersion(t *testing.T) {
	for _, version := range []string{"", "version: 1\n"} {
		if _, _, err := resolve(t, t.TempDir(), version); err != nil {
			t.Errorf("%q rejected: %v", version, err)
		}
	}
	wantProblems(t, t.TempDir(), "version: 2\n", "unsupported version 2")
}

func TestResolveReportsEveryProblemAtOnce(t *testing.T) {
	_, _, err := resolve(t, t.TempDir(), `
agents:
  a: { harness: nope }
  b: { permissions: weird }
`)
	if err == nil {
		t.Fatal("expected problems")
	}
	if n := len(strings.Split(err.Error(), "\n")); n < 3 {
		t.Errorf("got %d problems, want them all reported:\n%v", n, err)
	}
}

func TestResolveAgentValidation(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       []string
	}{
		{"harness is required", "agents:\n  a: {}\n", []string{"harness is required"}},
		{"unknown harness", "agents:\n  a: { harness: gpt }\n", []string{`unknown harness "gpt"`}},
		{"custom needs an image", "agents:\n  a: { harness: custom }\n", []string{"custom harness needs an image"}},
		{"unknown permissions", "agents:\n  a: { harness: custom, image: x, permissions: yolo }\n", []string{`unknown permissions "yolo"`}},
		{"agent names cannot contain a slash", "agents:\n  a/b: { harness: custom, image: x }\n", []string{"a name is 1 to 63 lowercase"}},
		{"unknown egress profile", "agents:\n  a: { harness: custom, image: x, egress: nowhere }\n", []string{`egress profile "nowhere" is not defined`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, t.TempDir(), c.yaml, c.want...) })
	}
}

func TestResolveAcceptsEveryPermissionAndHarness(t *testing.T) {
	for _, harness := range HarnessNames() {
		for _, permissions := range []string{"", "bypass", "default"} {
			yaml := "agents:\n  a: { harness: " + harness + ", image: img, permissions: '" + permissions + "' }\n"
			if _, _, err := resolve(t, t.TempDir(), yaml); err != nil {
				t.Errorf("harness %s permissions %q: %v", harness, permissions, err)
			}
		}
	}
}

func TestResolveWarnsWhenTheHarnessCannotReachItsProvider(t *testing.T) {
	_, warnings := mustResolve(t, t.TempDir(), "agents:\n  coder: { harness: claude-code }\n")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "api.anthropic.com") || !strings.Contains(warnings[0], `"coder"`) {
		t.Errorf("warnings = %q", warnings)
	}

	_, warnings = mustResolve(t, t.TempDir(), "agents:\n  coder: { harness: custom, image: x }\n")
	if len(warnings) != 0 {
		t.Errorf("custom harness warned: %q", warnings)
	}

	_, warnings = mustResolve(t, t.TempDir(), `
egress:
  default: { allow: ["*.anthropic.com"] }
agents:
  coder: { harness: claude-code }
`)
	if len(warnings) != 0 {
		t.Errorf("wildcard allow still warned: %q", warnings)
	}

	for _, hosts := range []string{"['*']", "['*.anthropic.com']"} {
		_, warnings = mustResolve(t, t.TempDir(), "egress:\n  default:\n    services:\n      web: { hosts: "+hosts+" }\nagents:\n  coder: { harness: claude-code }\n")
		if len(warnings) != 0 {
			t.Errorf("wildcard service %s still warned: %q", hosts, warnings)
		}
	}
	_, warnings = mustResolve(t, t.TempDir(), "egress:\n  default:\n    services:\n      web: { hosts: ['*.example.com'] }\nagents:\n  coder: { harness: claude-code }\n")
	if len(warnings) != 1 {
		t.Errorf("an unrelated wildcard service silenced the warning: %q", warnings)
	}
}

func TestResolveVaults(t *testing.T) {
	t.Run("a reference declares its secret; the vault lists them sorted", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), `
vaults:
  main: { backend: env }
  spare: { backend: pass }
egress:
  default:
    services:
      b: { hosts: [b.example.com], inject: { header: X-Key }, secret: main/B }
      a: { hosts: [a.example.com], inject: { header: X-Key }, secret: main/A }
  other:
    services:
      again: { hosts: [c.example.com], inject: { header: X-Key }, secret: main/A }
`)
		if want := (ResolvedVault{Backend: "env", Secrets: []string{"A", "B"}}); !reflect.DeepEqual(resolved.Vaults["main"], want) {
			t.Errorf("vault = %v, want %v", resolved.Vaults["main"], want)
		}
		if want := (SecretRef{Vault: "main", Name: "A", Backend: "env"}); resolved.Secrets["main/A"] != want || len(resolved.Secrets) != 2 {
			t.Errorf("secrets = %v", resolved.Secrets)
		}
		if want := []string{`warning: vault "spare": nothing refers to a secret in it`}; !reflect.DeepEqual(resolved.UnusedVaults(), want) {
			t.Errorf("warnings = %q, want %q", resolved.UnusedVaults(), want)
		}
	})

	vaultCases := []struct {
		name, yaml, want string
	}{
		{"slash in vault name", "vaults:\n  a/b: { backend: pass }\n", "names cannot contain '/'"},
		{"no backend", "vaults:\n  v: {}\n", "backend is required"},
		{"unknown backend", "vaults:\n  v: { backend: vault }\n", `unknown backend "vault" (backends: env, pass)`},
		{"file is not a backend", "vaults:\n  v: { backend: file }\n", `unknown backend "file" (backends: env, pass)`},
	}
	for _, c := range vaultCases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, t.TempDir(), c.yaml, c.want) })
	}

	nameCases := []struct {
		name, backend, secret, want string
	}{
		{"slash in env secret name", "env", "a/b", `"a/b"`},
		{"env secret name with a dash", "env", "MY-TOKEN", `"MY-TOKEN"`},
		{"env secret name with a leading digit", "env", "1TOKEN", `"1TOKEN"`},
		{"absolute pass name", "pass", "/abs", `"/abs"`},
		{"pass name ending in a slash", "pass", "a/", `"a/"`},
		{"empty pass segment", "pass", "a//b", `"a//b"`},
		{"dot pass segment", "pass", "a/./b", `"a/./b"`},
		{"pass name starting with a dash", "pass", "-c", `"-c"`},
		{"pass name that is an option", "pass", "--version", `"--version"`},
		{"pass name with a newline", "pass", "a\\nb", "\"a\\nb\""},
		{"pass name with a NUL", "pass", "a\\0b", "\"a\\x00b\""},
		{"dot dot pass segment", "pass", "../x", `"../x"`},
	}
	for _, c := range nameCases {
		t.Run(c.name, func(t *testing.T) {
			yaml := "vaults:\n  v: { backend: " + c.backend + " }\negress:\n  default:\n    services:\n      s: { hosts: [s.example.com], inject: { header: X-Key }, secret: \"v/" + c.secret + "\" }\n"
			wantProblems(t, t.TempDir(), yaml, c.want)
		})
	}

	t.Run("a reference to an unknown vault names the vaults that exist", func(t *testing.T) {
		wantProblems(t, t.TempDir(), "vaults:\n  a: { backend: env }\n  b: { backend: env }\negress:\n  default:\n    services:\n      anthropic: ghost/K\n",
			`names unknown vault "ghost" (vaults: a, b)`)
	})
}

func TestVaultsAreDecodedStrictly(t *testing.T) {
	for _, bad := range []string{
		"vaults:\n  v: { backend: pass, store: /x }\n",
		"vaults:\n  v: { backend: pass, path: /x }\n",
		"vaults:\n  v: { backend: env, secrets: { S: { from: 'env:S' } } }\n",
		"vaults:\n  v: { backend: env, secrets: [S] }\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestPassSecretNamesAreHierarchicalAndReferencesSplitAtTheFirstSlash(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
vaults:
  main: { backend: pass }
egress:
  default:
    services:
      deploy: { hosts: [api.example.com], inject: { header: X-Key }, secret: main/company/project/test }
`)
	if want := (SecretRef{Vault: "main", Name: "company/project/test", Backend: "pass"}); resolved.Secrets["main/company/project/test"] != want {
		t.Errorf("secrets = %v", resolved.Secrets)
	}
}

func TestResolveRejectsSecretLookingEnv(t *testing.T) {
	cases := []struct {
		name, key, value string
		rejected         bool
	}{
		{"anthropic style key", "FOO", "sk-ant-abcdefghijklmnopqrstuvwxyz", true},
		{"github token", "FOO", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", true},
		{"fine grained github token", "FOO", "github_pat_abcdefghijklmnopqrstuvwxyz", true},
		{"slack token", "FOO", "xoxb-1234567890-abcdef", true},
		{"aws key id", "FOO", "AKIAABCDEFGHIJKLMNOP", true},
		{"private key", "FOO", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"sensitive name with opaque value", "API_TOKEN", "abcdef0123456789abcdef", true},
		{"sensitive name with short value", "API_TOKEN", "short", false},
		{"sensitive name with plain words", "PASSWORD_POLICY", "must be strong", false},
		{"harmless", "LOG_LEVEL", "debug", false},
		{"long harmless value", "GREETING", "hello-there-this-is-long-but-fine", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := resolve(t, t.TempDir(), "agents:\n  a:\n    harness: custom\n    image: x\n    env:\n      "+c.key+": '"+c.value+"'\n")
			if (err != nil) != c.rejected {
				t.Fatalf("error = %v, rejected want %v", err, c.rejected)
			}
			if err != nil && strings.Contains(err.Error(), c.value) {
				t.Errorf("the error leaks the value: %v", err)
			}
		})
	}
}

func TestResolveEnvProblemsAreReportedInSortedOrder(t *testing.T) {
	_, _, err := resolve(t, t.TempDir(), `
agents:
  a:
    harness: custom
    image: x
    env:
      Z_KEY: sk-ant-abcdefghijklmnopqrstuvwxyz
      A_KEY: sk-ant-abcdefghijklmnopqrstuvwxyz
`)
	if err == nil {
		t.Fatal("expected problems")
	}
	if a, z := strings.Index(err.Error(), "A_KEY"), strings.Index(err.Error(), "Z_KEY"); a < 0 || z < 0 || a > z {
		t.Errorf("order = %q", err)
	}
}

func TestResolveWorkspaces(t *testing.T) {
	dir := t.TempDir()

	t.Run("a workspace without a source is a directory kept in a volume", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, "workspaces:\n  scratch: {}\n")
		if got := resolved.Workspaces["scratch"]; got != (ResolvedWorkspace{}) {
			t.Errorf("scratch = %+v, want the empty workspace", got)
		}
	})

	t.Run("a git workspace keeps its source", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, "workspaces:\n  repo:\n    git: { url: https://github.com/o/r.git, branch: main }\n")
		if got := resolved.Workspaces["repo"]; got.Git == nil || got.Git.URL != "https://github.com/o/r.git" || got.Git.Branch != "main" {
			t.Errorf("repo = %+v", got)
		}
	})

	cases := []struct {
		name, yaml, want string
	}{
		{"bad name", "workspaces:\n  'a:b': {}\n", "a name is 1 to 63 lowercase"},
		{"ssh url", "workspaces:\n  w: { git: { url: 'git@github.com:o/r.git' } }\n", "must be an https URL"},
		{"local path url", "workspaces:\n  w: { git: { url: /srv/repo } }\n", "must be an https URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, dir, c.yaml, c.want) })
	}

	t.Run("https scheme is case insensitive", func(t *testing.T) {
		mustResolve(t, dir, "workspaces:\n  w: { git: { url: 'HTTPS://x/y' } }\n")
	})
}

func TestResolveMounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "afile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("single workspace becomes the working directory", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
workspaces: { shared: {} }
agents:
  a: { harness: custom, image: x, workspaces: [shared] }
`)
		agent := resolved.Agents["a"]
		want := []Mount{{Name: "shared", Mount: "/workspace/shared", Mode: "rw"}}
		if !reflect.DeepEqual(agent.Workspaces, want) {
			t.Errorf("mounts = %+v, want %+v", agent.Workspaces, want)
		}
		if agent.Workdir != "/workspace/shared" {
			t.Errorf("workdir = %q", agent.Workdir)
		}
	})

	t.Run("several workspaces leave the working directory at the root", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
workspaces: { one: {}, two: {} }
agents:
  a: { harness: custom, image: x, workspaces: [one, "two:ro"] }
`)
		agent := resolved.Agents["a"]
		if agent.Workdir != "/workspace" {
			t.Errorf("workdir = %q", agent.Workdir)
		}
		if agent.Workspaces[1].Mode != "ro" {
			t.Errorf("second mount = %+v, want ro", agent.Workspaces[1])
		}
	})

	t.Run("a directory workspace can be read-only", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
workspaces: { docs: {} }
agents:
  a: { harness: custom, image: x, workspaces: ["docs:ro"] }
`)
		want := Mount{Name: "docs", Mount: "/workspace/docs", Mode: "ro"}
		if got := resolved.Agents["a"].Workspaces[0]; got != want {
			t.Errorf("mount = %+v, want %+v", got, want)
		}
	})

	t.Run("a git workspace is mounted per instance", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
workspaces: { repo: { git: { url: https://x/y.git } } }
agents:
  a: { harness: custom, image: x, workspaces: [repo] }
`)
		want := Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}
		if got := resolved.Agents["a"].Workspaces[0]; got != want {
			t.Errorf("mount = %+v, want %+v", got, want)
		}
	})

	t.Run("workdir", func(t *testing.T) {
		base := `
workspaces: { one: {}, two: {} }
agents:
  a:
    harness: custom
    image: x
    workspaces: [one, two]
    workdir: %s
`
		for workdir, want := range map[string]string{
			"one":             "/workspace/one",
			"/one/":           "/one",
			"/opt/../srv/app": "/srv/app",
		} {
			resolved, _ := mustResolve(t, dir, strings.Replace(base, "%s", workdir, 1))
			if got := resolved.Agents["a"].Workdir; got != want {
				t.Errorf("workdir %q resolved to %q, want %q", workdir, got, want)
			}
		}
		wantProblems(t, dir, strings.Replace(base, "%s", "three", 1), `workdir "three" is not one of its workspaces (one, two)`)
	})

	cases := []struct {
		name, yaml, want string
	}{
		{"malformed reference", "agents:\n  a: { harness: custom, image: x, workspaces: ['a:b:c'] }\n", "unsupported workspace reference"},
		{"undeclared workspace", "agents:\n  a: { harness: custom, image: x, workspaces: [ghost] }\n", `workspace "ghost" is not declared`},
		{"an inline path is not a workspace", "agents:\n  a: { harness: custom, image: x, workspaces: ['./nope'] }\n", "declare it once under workspaces:"},
		{"read-only git workspace", "workspaces:\n  r: { git: { url: https://x/y } }\nagents:\n  a: { harness: custom, image: x, workspaces: ['r:ro'] }\n", "cannot be read-only"},
		{"read-only git workspace points at the alternatives", "workspaces:\n  r: { git: { url: https://x/y } }\nagents:\n  a: { harness: custom, image: x, workspaces: ['r:ro'] }\n", "declare a directory workspace"},
		{"mount collision", "workspaces: { w: {} }\nagents:\n  a: { harness: custom, image: x, workspaces: [w, 'w'] }\n", "two workspaces mount at /workspace/w"},
		{"another agent's checkout cannot be mounted", "workspaces: { w: {} }\nagents:\n  a: { harness: custom, image: x, workspaces: [w] }\n  b: { harness: custom, image: x, workspaces: ['a/w:ro'] }\n", "unsupported workspace reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, dir, c.yaml, c.want) })
	}
}

func TestInjectDefaultsDependOnHowTheHarnessReportsItsState(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
agents:
  reporting: {harness: claude-code}
  plain: {harness: custom, image: x}
egress:
  default: {allow: [platform.claude.com]}
`)
	reporting, plain := resolved.Agents["reporting"].Inject, resolved.Agents["plain"].Inject
	if reporting.IdleSignal != "state" || plain.IdleSignal != "quiescence" {
		t.Errorf("reporting = %+v plain = %+v", reporting, plain)
	}
	if reporting.HumanQuiet != "30s" || reporting.AckTimeout != "60s" || reporting.Quiescence != "5s" {
		t.Errorf("defaults = %+v", reporting)
	}
}

func TestInjectSettingsOverrideTheDefaultsAndAreValidated(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
agents:
  coder: {harness: custom, image: x, inject: {human_quiet: 2m, idle_signal: state}}
`)
	inject := resolved.Agents["coder"].Inject
	if inject.HumanQuiet != "2m" || inject.IdleSignal != "state" || inject.AckTimeout != "60s" {
		t.Errorf("inject = %+v", inject)
	}
	for _, bad := range []string{"human_quiet: soon", "ack_timeout: -3s", "idle_signal: vibes", "quiescence: 0s"} {
		file, err := Parse([]byte("agents:\n  coder: {harness: custom, image: x, inject: {" + bad + "}}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Resolve(file, "p", t.TempDir()); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if _, err := Parse([]byte("agents:\n  coder: {harness: custom, image: x, inject: {nonsense: 1}}\n")); err == nil {
		t.Error("an unknown inject key was accepted")
	}
}

func TestAPromptThatIsNotAFileIsRejectedAndOneThatIsIsAccepted(t *testing.T) {
	dir := t.TempDir()
	yaml := "agents:\n  coder: {harness: custom, image: x, prompt: ./prompts/coder.md}\n"
	if _, _, err := resolve(t, dir, yaml); err == nil || !strings.Contains(err.Error(), "prompts/coder.md") {
		t.Fatalf("a missing prompt: err = %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("be brief\n"), 0o644)
	if _, _, err := resolve(t, dir, yaml); err != nil {
		t.Errorf("an existing prompt: err = %v", err)
	}
	os.Remove(filepath.Join(dir, "prompts", "coder.md"))
	os.MkdirAll(filepath.Join(dir, "prompts", "coder.md"), 0o755)
	if _, _, err := resolve(t, dir, yaml); err == nil {
		t.Error("a directory was accepted as a prompt")
	}
}

func TestDependsOnIsNotAKey(t *testing.T) {
	_, err := Parse([]byte("agents:\n  a: { harness: custom, image: x, depends_on: [b] }\n  b: { harness: custom, image: x }\n"))
	if err == nil || !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("depends_on was accepted or not named: %v", err)
	}
}
