package config

import (
	"strings"
	"testing"
)

func TestReservedAndMalformedNames(t *testing.T) {
	custom := "{ harness: custom, image: x }"
	cases := []struct{ name, yaml, want string }{
		{"agent control", "agents:\n  control: " + custom + "\n", "reserved"},
		{"agent proxy", "agents:\n  proxy: " + custom + "\n", "reserved"},
		{"agent prep", "agents:\n  prep: " + custom + "\n", "reserved"},
		{"agent shared", "agents:\n  shared: " + custom + "\n", "reserved"},
		{"agent with a space", "agents:\n  'my agent': " + custom + "\n", "lowercase"},
		{"agent in capitals", "agents:\n  Coder: " + custom + "\n", "lowercase"},
		{"agent too long", "agents:\n  " + strings.Repeat("a", 64) + ": " + custom + "\n", "lowercase"},
		{"workspace control", "workspaces:\n  control: {}\n", "reserved"},
		{"workspace ca", "workspaces:\n  ca: {}\n", "reserved"},
		{"workspace ca-private", "workspaces:\n  ca-private: {}\n", "reserved"},
		{"workspace egress", "workspaces:\n  egress: {}\n", "reserved"},
		{"workspace dotdot", "workspaces:\n  '..': {}\n", "lowercase"},
		{"home of an agent", "agents:\n  coder: " + custom + "\nworkspaces:\n  coder-home: {}\n", "home of agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, t.TempDir(), c.yaml, c.want) })
	}
	t.Run("a -home workspace without such an agent is fine", func(t *testing.T) {
		mustResolve(t, t.TempDir(), "workspaces:\n  data-home: {}\n")
	})
}

func TestInstanceNames(t *testing.T) {
	templates := []string{"coder", "reviewer"}
	for _, name := range []string{"issue-412", "coder-1", "a", "pr_88", "x" + strings.Repeat("y", 62)} {
		if err := CheckInstanceName(name, templates); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for name, want := range map[string]string{
		"":                            "invalid instance name",
		"Issue":                       "invalid instance name",
		"-x":                          "invalid instance name",
		"a/b":                         "invalid instance name",
		"x" + strings.Repeat("y", 63): "invalid instance name",
		"coder":                       "name of a template",
		"control":                     "reserved",
		"proxy":                       "reserved",
		"prep":                        "reserved",
		"shared":                      "reserved",
		"control-1":                   "name of a sidecar",
		"proxy-1":                     "name of a sidecar",
		"coder-home":                  "-home",
	} {
		err := CheckInstanceName(name, templates)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want it to mention %q", name, err, want)
		}
	}
}

func TestEnvOwnedByEgzoCannotBeOverridden(t *testing.T) {
	for _, key := range []string{"EGZO_TOKEN", "EGZO_ANYTHING", "HTTPS_PROXY", "https_proxy", "No_Proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		t.Run(key, func(t *testing.T) {
			wantProblems(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, env: { "+key+": v } }\n", key)
		})
	}
	mustResolve(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, env: { FOO: v, EGZOISH: v } }\n")
}

func TestGitSourceRules(t *testing.T) {
	ws := func(git string) string { return "workspaces:\n  w: { git: " + git + " }\n" }
	t.Run("credentials are refused and never repeated", func(t *testing.T) {
		_, _, err := resolve(t, t.TempDir(), ws("{ url: 'https://bob:hunter2secret@host/o/r.git' }"))
		if err == nil || !strings.Contains(err.Error(), "credentials") {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "bob") {
			t.Errorf("the error repeats the credential: %v", err)
		}
	})
	t.Run("a token as the user name is a credential too", func(t *testing.T) {
		wantProblems(t, t.TempDir(), ws("{ url: 'https://ghp_x@host/o/r.git' }"), "credentials")
	})
	t.Run("a non-https url does not leak userinfo either", func(t *testing.T) {
		_, _, err := resolve(t, t.TempDir(), ws("{ url: 'http://bob:pw123@host/o/r.git' }"))
		if err == nil || strings.Contains(err.Error(), "pw123") {
			t.Fatalf("error = %v", err)
		}
	})
	for _, branch := range []string{"--detach", "-x", "a b", "a..b", "x~1", "x^", "x:y", "x?", "x*", "x[", "a\\\\b", "a.lock", "a/", "/a", "a//b", "a@{b", "@", "a/.b", "a/b.lock/c", "a."} {
		t.Run("bad branch "+branch, func(t *testing.T) {
			wantProblems(t, t.TempDir(), ws("{ url: 'https://h/o/r', branch: \""+branch+"\" }"), "not a valid git branch")
		})
	}
	for _, branch := range []string{"main", "release/1.2", "feature/x_y-z", "v1.0.0", "ünï"} {
		t.Run("good branch "+branch, func(t *testing.T) {
			mustResolve(t, t.TempDir(), ws("{ url: 'https://h/o/r', branch: \""+branch+"\" }"))
		})
	}
}

func TestResourcesAreValidatedWhenTheFileIsRead(t *testing.T) {
	cases := []struct{ resources, want string }{
		{"{ cpus: -1 }", "resources.cpus"},
		{"{ memory: lots }", "resources.memory"},
		{"{ memory: -4g }", "resources.memory"},
	}
	for _, c := range cases {
		wantProblems(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, resources: "+c.resources+" }\n", c.want)
	}
	mustResolve(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, resources: { cpus: 1.5, memory: 512m } }\n")
}

func TestWorkdirCannotClimbOut(t *testing.T) {
	yaml := func(workdir string) string {
		return "workspaces:\n  repo: {}\nagents:\n  a: { harness: custom, image: x, workspaces: [repo], workdir: '" + workdir + "' }\n"
	}
	wantProblems(t, t.TempDir(), yaml("repo/../../etc"), "workdir")
	wantProblems(t, t.TempDir(), yaml("../x"), "workdir")
	resolved, _ := mustResolve(t, t.TempDir(), yaml("repo/sub/./dir"))
	if got := resolved.Agents["a"].Workdir; got != "/workspace/repo/sub/dir" {
		t.Errorf("workdir = %q", got)
	}
}

func TestHostMountsOfTheFilesystemAndTheEngineSocket(t *testing.T) {
	wantProblems(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, workspaces: [/] }\n", "filesystem root")
	_, warnings := mustResolve(t, t.TempDir(), "agents:\n  a: { harness: custom, image: x, workspaces: [/var/run] }\n")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "docker.sock") {
		t.Errorf("warnings = %q", warnings)
	}
	if socketWarning("/home/me") != "" || socketWarning("/var/runner") != "" {
		t.Error("an unrelated directory was flagged")
	}
	if socketWarning("/run") == "" || socketWarning("/var") == "" || socketWarning("/var/run/docker.sock") == "" {
		t.Error("a directory above or at the socket was not flagged")
	}
}

func TestHostsAreNormalised(t *testing.T) {
	resolved, warnings := mustResolve(t, t.TempDir(), `
egress:
  default:
    allow: [Platform.Claude.COM., "*.PyPI.org"]
    services:
      mine: { hosts: [API.Example.com.], inject: { header: x-key }, secret: main/K }
vaults:
  main: { secrets: { K: { from: "env:K" } } }
agents:
  a: { harness: claude-code }
`)
	profile := resolved.Egress["default"]
	if got := strings.Join(profile.Allow, ","); got != "platform.claude.com,*.pypi.org" {
		t.Errorf("allow = %s", got)
	}
	if got := profile.Services["mine"].Hosts[0]; got != "api.example.com" {
		t.Errorf("service host = %s", got)
	}
	_ = warnings
}

func TestASecretCannotBeBoundToAServiceThatInjectsNothing(t *testing.T) {
	wantProblems(t, t.TempDir(), `
vaults:
  main: { secrets: { K: { from: "env:K" } } }
egress:
  base: { services: { plain: { hosts: [example.com] } } }
  child: { extend: base, services: { plain: main/K } }
`, "injects no credential")
}

func TestUsersOnlyRejectedWhereASectionCouldBeDeclared(t *testing.T) {
	for _, ok := range []string{
		"workspaces:\n  users: {}\n",
		"agents:\n  a: { harness: custom, image: x, env: { users: '4' } }\n",
		"vaults:\n  users: { secrets: { users: { from: 'env:U' } } }\n",
	} {
		if _, err := Parse([]byte(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"users: {}\n",
		"agents:\n  a:\n    users: []\n",
		"egress:\n  p:\n    users: []\n",
		"vaults:\n  v:\n    users: []\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "users are never declared") {
			t.Errorf("%q: error = %v", bad, err)
		}
	}
}

func TestSettingsThatDoNothingAreRejected(t *testing.T) {
	for _, bad := range []string{
		"proxy: { audit: false }\n",
		"control: { tools: [status] }\n",
		"agents:\n  a: { harness: custom, image: x, tools: [control] }\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	resolved, _ := mustResolve(t, t.TempDir(), "proxy: { image: r/e:1 }\n")
	if resolved.Proxy == nil || resolved.Proxy.Image != "r/e:1" {
		t.Errorf("proxy = %+v", resolved.Proxy)
	}
	if resolved, _ := mustResolve(t, t.TempDir(), "{}\n"); resolved.Proxy != nil {
		t.Errorf("proxy = %+v, want nil when the file says nothing", resolved.Proxy)
	}
}

func TestOpenCodeWithASubscriptionTokenIsWarnedAbout(t *testing.T) {
	yaml := func(service, harness string) string {
		return "vaults:\n  main: { secrets: { K: { from: 'env:K' } } }\negress:\n  default:\n    allow: [models.opencode.ai]\n    services:\n      " + service + ": main/K\nagents:\n  a: { harness: " + harness + " }\n"
	}
	_, warnings := mustResolve(t, t.TempDir(), yaml("anthropic-oauth", "opencode"))
	found := false
	for _, w := range warnings {
		found = found || strings.Contains(w, "opencode") && strings.Contains(w, "bearer")
	}
	if !found {
		t.Errorf("warnings = %q", warnings)
	}
	_, warnings = mustResolve(t, t.TempDir(), yaml("anthropic", "opencode"))
	for _, w := range warnings {
		if strings.Contains(w, "bearer") {
			t.Errorf("an API key was flagged: %q", w)
		}
	}
	_, warnings = mustResolve(t, t.TempDir(), yaml("anthropic-oauth", "claude-code"))
	for _, w := range warnings {
		if strings.Contains(w, "bearer") {
			t.Errorf("claude-code takes a subscription token: %q", w)
		}
	}
}
