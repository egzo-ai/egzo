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
}

func TestResolveVaults(t *testing.T) {
	t.Run("lists sorted secret names and keeps sources out of the printed view", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), `
vaults:
  main:
    backend: env
    secrets:
      B: { from: "env:B" }
      A: { from: "file:~/a" }
`)
		if want := []string{"A", "B"}; !reflect.DeepEqual(resolved.Vaults["main"], want) {
			t.Errorf("vault secrets = %v, want %v", resolved.Vaults["main"], want)
		}
		if resolved.SecretSources["main/A"] != "file:~/a" || resolved.SecretSources["main/B"] != "env:B" {
			t.Errorf("sources = %v", resolved.SecretSources)
		}
	})

	cases := []struct {
		name, yaml, want string
	}{
		{"slash in vault name", "vaults:\n  a/b: {}\n", "names cannot contain '/'"},
		{"unknown backend", "vaults:\n  v: { backend: vault }\n", `unknown backend "vault"`},
		{"slash in secret name", "vaults:\n  v:\n    secrets:\n      a/b: { from: 'env:X' }\n", "secret name"},
		{"missing from", "vaults:\n  v:\n    secrets:\n      S: {}\n", "needs from:"},
		{"empty location", "vaults:\n  v:\n    secrets:\n      S: { from: 'env:' }\n", "needs from:"},
		{"no scheme", "vaults:\n  v:\n    secrets:\n      S: { from: plain }\n", "needs from:"},
		{"unsupported scheme", "vaults:\n  v:\n    secrets:\n      S: { from: 'sops:x' }\n", `unsupported source scheme "sops"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, t.TempDir(), c.yaml, c.want) })
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

	t.Run("volume workspace has no source", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, "workspaces:\n  scratch: {}\n")
		if got := resolved.Workspaces["scratch"]; got != (ResolvedWorkspace{}) {
			t.Errorf("scratch = %+v", got)
		}
	})

	t.Run("git workspace defaults", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, "workspaces:\n  repo:\n    git: { url: https://github.com/o/r.git }\n")
		got := resolved.Workspaces["repo"]
		if got.Mode != "clone" {
			t.Errorf("mode = %q, want clone", got.Mode)
		}
		if want := filepath.Join(dir, ".egzo", "workspaces", "repo"); got.Path != want {
			t.Errorf("path = %q, want %q", got.Path, want)
		}
	})

	t.Run("relative and absolute paths", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
workspaces:
  rel: { git: { url: https://x/y.git }, path: ./sub/../checkout }
  abs: { git: { url: https://x/y.git }, path: /srv/abs/ }
`)
		if want := filepath.Join(dir, "checkout"); resolved.Workspaces["rel"].Path != want {
			t.Errorf("rel = %q, want %q", resolved.Workspaces["rel"].Path, want)
		}
		if resolved.Workspaces["abs"].Path != "/srv/abs" {
			t.Errorf("abs = %q", resolved.Workspaces["abs"].Path)
		}
	})

	cases := []struct {
		name, yaml, want string
	}{
		{"bad name", "workspaces:\n  'a:b': {}\n", "a name is 1 to 63 lowercase"},
		{"mode without git", "workspaces:\n  w: { mode: clone }\n", "mode only applies to git"},
		{"path without git", "workspaces:\n  w: { path: ./x }\n", "path only applies to git"},
		{"ssh url", "workspaces:\n  w: { git: { url: 'git@github.com:o/r.git' } }\n", "must be an https URL"},
		{"local path url", "workspaces:\n  w: { git: { url: /srv/repo } }\n", "must be an https URL"},
		{"unknown mode", "workspaces:\n  w: { git: { url: https://x/y }, mode: copy }\n", `unknown mode "copy"`},
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

	t.Run("host directories mount by their base name", func(t *testing.T) {
		resolved, _ := mustResolve(t, dir, `
agents:
  a: { harness: custom, image: x, workspaces: ["./docs:ro"] }
`)
		want := Mount{Name: "docs", Mount: "/workspace/docs", Mode: "ro", HostPath: filepath.Join(dir, "docs")}
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
		{"missing host directory", "agents:\n  a: { harness: custom, image: x, workspaces: ['./nope'] }\n", "not an existing directory"},
		{"host path is a file", "agents:\n  a: { harness: custom, image: x, workspaces: ['./afile'] }\n", "not an existing directory"},
		{"read-only git workspace", "workspaces:\n  r: { git: { url: https://x/y } }\nagents:\n  a: { harness: custom, image: x, workspaces: ['r:ro'] }\n", "cannot be read-only"},
		{"read-only git workspace points at the alternatives", "workspaces:\n  r: { git: { url: https://x/y } }\nagents:\n  a: { harness: custom, image: x, workspaces: ['r:ro'] }\n", "host path (./path:ro) or a shared workspace"},
		{"mount collision", "workspaces: { w: {} }\nagents:\n  a: { harness: custom, image: x, workspaces: [w, './docs', 'w'] }\n", "two workspaces mount at /workspace/w"},
		{"another agent's checkout cannot be mounted", "workspaces: { w: {} }\nagents:\n  a: { harness: custom, image: x, workspaces: [w] }\n  b: { harness: custom, image: x, workspaces: ['a/w:ro'] }\n", "unsupported workspace reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, dir, c.yaml, c.want) })
	}
}

func TestInjectDefaultsDependOnHowTheHarnessReportsItsState(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
agents:
  hooked: {harness: claude-code}
  plain: {harness: custom, image: x}
egress:
  default: {allow: [platform.claude.com]}
`)
	hooked, plain := resolved.Agents["hooked"].Inject, resolved.Agents["plain"].Inject
	if hooked.IdleSignal != "hook" || plain.IdleSignal != "quiescence" {
		t.Errorf("hooked = %+v plain = %+v", hooked, plain)
	}
	if hooked.HumanQuiet != "30s" || hooked.AckTimeout != "60s" || hooked.Quiescence != "5s" {
		t.Errorf("defaults = %+v", hooked)
	}
}

func TestInjectSettingsOverrideTheDefaultsAndAreValidated(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
agents:
  coder: {harness: custom, image: x, inject: {human_quiet: 2m, idle_signal: hook}}
`)
	inject := resolved.Agents["coder"].Inject
	if inject.HumanQuiet != "2m" || inject.IdleSignal != "hook" || inject.AckTimeout != "60s" {
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
