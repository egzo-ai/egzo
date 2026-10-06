package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

func gitProject(t *testing.T, mode string) (*config.Resolved, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, ".egzo", "workspaces", "repo")
	mount := config.Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}
	// The agents are instances: each one's checkout is a directory named after it.
	return &config.Resolved{
		Name: "proj",
		Workspaces: map[string]config.ResolvedWorkspace{
			"repo": {Git: &config.Git{URL: "https://github.com/acme/shop.git", Branch: "main"}, Mode: mode, Path: path},
		},
		Agents: map[string]config.ResolvedAgent{
			"coder":  {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"tester": {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"docs":   {Harness: "custom", Image: "x", Workdir: "/workspace", Workspaces: []config.Mount{{Name: "docs", Mount: "/workspace/docs", Mode: "ro", HostPath: "/host/docs"}}},
		},
	}, path
}

func TestCloneModePlansOneCheckoutPerAgentThatListsTheWorkspace(t *testing.T) {
	project, path := gitProject(t, "clone")
	plan, err := PlanGit(project)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, checkout := range plan {
		dirs = append(dirs, checkout.Dir)
		if checkout.URL != "https://github.com/acme/shop.git" || checkout.Branch != "main" || checkout.Workspace != "repo" {
			t.Errorf("checkout = %+v", checkout)
		}
	}
	if want := []string{filepath.Join(path, "coder"), filepath.Join(path, "tester")}; !slices.Equal(dirs, want) {
		t.Errorf("dirs = %v, want %v (a host path makes no checkout)", dirs, want)
	}
}

func TestSharedModePlansOneCheckoutThroughTheFirstAgent(t *testing.T) {
	project, path := gitProject(t, "shared")
	plan, _ := PlanGit(project)
	if len(plan) != 1 || plan[0].Dir != filepath.Join(path, "shared") || plan[0].Agent != "coder" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestWorktreeModePlansAWorktreePerAgentAndABase(t *testing.T) {
	project, path := gitProject(t, "worktree")
	plan, _ := PlanGit(project)
	if len(plan) != 2 || plan[0].Base != filepath.Join(path, ".base") || plan[1].Dir != filepath.Join(path, "tester") {
		t.Errorf("plan = %+v", plan)
	}
}

func TestAnExistingCheckoutIsNotPlannedAgain(t *testing.T) {
	project, path := gitProject(t, "clone")
	os.MkdirAll(filepath.Join(path, "coder", ".git"), 0o755)
	plan, _ := PlanGit(project)
	if len(plan) != 1 || plan[0].Agent != "tester" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestACheckoutMadeInAnotherModeIsRefusedNotConverted(t *testing.T) {
	for have, want := range map[string]string{"clone": "shared", "shared": "clone", "worktree": "clone"} {
		project, path := gitProject(t, want)
		switch have {
		case "clone":
			os.MkdirAll(filepath.Join(path, "coder", ".git"), 0o755)
		case "shared":
			os.MkdirAll(filepath.Join(path, "shared", ".git"), 0o755)
		case "worktree":
			os.MkdirAll(filepath.Join(path, ".base", ".git"), 0o755)
		}
		_, err := PlanGit(project)
		if err == nil || !strings.Contains(err.Error(), "mode "+want) || !strings.Contains(err.Error(), have) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s checkouts under mode %s: err = %v", have, want, err)
		}
	}
}

func TestAWorkspaceNoAgentListsIsLeftAlone(t *testing.T) {
	project, _ := gitProject(t, "clone")
	for name, agent := range project.Agents {
		agent.Workspaces = nil
		project.Agents[name] = agent
	}
	if plan, _ := PlanGit(project); len(plan) != 0 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestGitInstancesMountTheirOwnCheckout(t *testing.T) {
	project, path := gitProject(t, "clone")
	d, err := desireErr(project, "/dir", Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "a", "tester": "b", "docs": "c"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coder", "tester"} {
		spec := findContainer(t, d, "proj-"+name)
		if want := (MountSpec{Bind: true, Source: filepath.Join(path, name), Target: "/workspace/repo"}); !slices.Contains(spec.Mounts, want) {
			t.Errorf("%s mounts = %+v", name, spec.Mounts)
		}
		for _, m := range spec.Mounts {
			if m.Bind && strings.HasPrefix(m.Source, path) && m.Source != filepath.Join(path, name) {
				t.Errorf("%s mounts another instance's checkout: %+v", name, m)
			}
		}
	}
}

func TestASharedCheckoutIsTheSameDirectoryForEveryInstance(t *testing.T) {
	project, path := gitProject(t, "shared")
	d, _ := desireErr(project, "/dir", Inputs{Image: "egzo:test", Tokens: map[string]string{}})
	for _, name := range []string{"coder", "tester"} {
		if want := (MountSpec{Bind: true, Source: filepath.Join(path, "shared"), Target: "/workspace/repo"}); !slices.Contains(findContainer(t, d, "proj-"+name).Mounts, want) {
			t.Errorf("%s does not mount the shared checkout: %+v", name, findContainer(t, d, "proj-"+name).Mounts)
		}
	}
}

func TestWorktreeAgentsMountTheirWorktreeAndTheBaseButNeverAnotherAgents(t *testing.T) {
	project, path := gitProject(t, "worktree")
	d, _ := desireErr(project, "/dir", Inputs{Image: "egzo:test", Tokens: map[string]string{}})
	coder := findContainer(t, d, "proj-coder")
	for _, want := range []MountSpec{
		{Bind: true, Source: filepath.Join(path, "coder"), Target: "/workspace/repo"},
		{Bind: true, Source: filepath.Join(path, ".base"), Target: "/.egzo/base/repo"},
	} {
		if !slices.Contains(coder.Mounts, want) {
			t.Errorf("coder lacks %+v: %+v", want, coder.Mounts)
		}
	}
	for _, m := range coder.Mounts {
		if strings.Contains(m.Source, "tester") {
			t.Errorf("coder mounts another agent's directory: %+v", m)
		}
	}
}

func TestGitDirsAreFoundOnDiskSoWorkOfRemovedInstancesIsFoundToo(t *testing.T) {
	project, path := gitProject(t, "worktree")
	for _, dir := range []string{"coder/.git", "gone/.git", ".base/.git", "notes", "empty"} {
		os.MkdirAll(filepath.Join(path, dir), 0o755)
	}
	os.Symlink("/", filepath.Join(path, "link"))
	// a checkout of the user's own that egzo did not make is not egzo's to offer for removal
	os.MkdirAll(filepath.Join(path, "mine", ".git"), 0o755)
	if err := markCheckouts(path, "coder", "gone", ".base", "link", "notes", "empty"); err != nil {
		t.Fatal(err)
	}
	got := GitDirs(project)
	for _, want := range []string{filepath.Join(path, "coder"), filepath.Join(path, "gone"), filepath.Join(path, ".base"), filepath.Join(path, "link")} {
		if !slices.Contains(got, want) {
			t.Errorf("GitDirs lacks %s: %v", want, got)
		}
	}
	for _, unwanted := range []string{filepath.Join(path, "notes"), filepath.Join(path, "empty"), filepath.Join(path, "mine")} {
		if slices.Contains(got, unwanted) {
			t.Errorf("GitDirs lists %s, which is not a checkout: %v", unwanted, got)
		}
	}
}

func TestTheRegistryOfCheckoutsIsAppendOnlyAndIgnoresWhatIsNotAName(t *testing.T) {
	path := t.TempDir()
	markCheckouts(path, "a", "b")
	markCheckouts(path, "b", "c")
	os.WriteFile(filepath.Join(path, checkoutRegistry), append(mustRead(t, filepath.Join(path, checkoutRegistry)), []byte("../etc\na/b\n..\n\n")...), 0o644)
	got := managedCheckouts(path)
	if len(got) != 3 || !got["a"] || !got["b"] || !got["c"] {
		t.Errorf("registry = %v, want exactly a, b, c: a path in it must never name something else", got)
	}
	count := 0
	for _, line := range strings.Split(string(mustRead(t, filepath.Join(path, checkoutRegistry))), "\n") {
		if line == "b" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("b was recorded %d times", count)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstanceGitDirsAreTheInstancesOwnCheckoutsOnly(t *testing.T) {
	for mode, want := range map[string]int{"clone": 1, "worktree": 1, "shared": 0} {
		project, path := gitProject(t, mode)
		dirs := InstanceGitDirs(project, "coder")
		if len(dirs) != want || (want == 1 && dirs[0] != filepath.Join(path, "coder")) {
			t.Errorf("%s: dirs = %v", mode, dirs)
		}
	}
	project, _ := gitProject(t, "clone")
	if dirs := InstanceGitDirs(project, "docs"); len(dirs) != 0 {
		t.Errorf("a host path is not a checkout: %v", dirs)
	}
	if dirs := InstanceGitDirs(project, "nobody"); len(dirs) != 0 {
		t.Errorf("an unknown instance has checkouts: %v", dirs)
	}
}

func TestUnsavedWorkIsDescribedWithItsDirectory(t *testing.T) {
	text := Unsaved{Dir: "/a/b", Uncommitted: 2, Unpushed: 1}.String()
	if !strings.Contains(text, "/a/b") || !strings.Contains(text, "2 uncommitted") || !strings.Contains(text, "1 unpushed") {
		t.Errorf("text = %q", text)
	}
}

func TestPodmanRefusesASpawnThatAsksForARuntime(t *testing.T) {
	err := refuseRuntime(&engine.Client{Podman: true}, "coder", "runsc")
	if err == nil || !strings.Contains(err.Error(), "coder") || !strings.Contains(err.Error(), "runsc") || !strings.Contains(err.Error(), "Podman") {
		t.Errorf("err = %v", err)
	}
	if err := refuseRuntime(&engine.Client{Podman: true}, "coder", ""); err != nil {
		t.Errorf("an agent without a runtime was refused: %v", err)
	}
	if err := refuseRuntime(&engine.Client{}, "coder", "runsc"); err != nil {
		t.Errorf("Docker applies the runtime itself: %v", err)
	}
}

func credProject(service string, def config.ResolvedService) *config.Resolved {
	copied := def
	return &config.Resolved{
		Agents: map[string]config.ResolvedAgent{"coder": {Egress: "default"}},
		Egress: map[string]*config.ResolvedProfile{"default": {Services: map[string]*config.ResolvedService{service: &copied}}},
	}
}

var (
	apiKeyService = config.ResolvedService{Hosts: []string{"api.anthropic.com"}, Secret: "main/K", Inject: &config.Inject{Header: "x-api-key"}}
	bearerService = config.ResolvedService{Hosts: []string{"api.anthropic.com"}, Secret: "main/K", Inject: &config.Inject{Header: "Authorization", Value: "Bearer {secret}"}}
)

func TestAnOAuthTokenSentAsAnAPIKeyIsRefusedWithoutShowingIt(t *testing.T) {
	problems := checkCredentialKinds(credProject("anthropic", apiKeyService), map[string]string{"main/K": "sk-ant-oat01-secretsecret"})
	if len(problems) != 1 || !strings.Contains(problems[0], "anthropic-oauth") || !strings.Contains(problems[0], "main/K") || strings.Contains(problems[0], "secretsecret") {
		t.Errorf("problems = %v", problems)
	}
}

func TestAnAPIKeySentAsABearerTokenIsRefused(t *testing.T) {
	problems := checkCredentialKinds(credProject("anthropic-oauth", bearerService), map[string]string{"main/K": "sk-ant-api03-secretsecret"})
	if len(problems) != 1 || !strings.Contains(problems[0], "API key") || strings.Contains(problems[0], "secretsecret") {
		t.Errorf("problems = %v", problems)
	}
}

func TestMatchingCredentialsAndOtherHostsAreLeftAlone(t *testing.T) {
	if p := checkCredentialKinds(credProject("anthropic", apiKeyService), map[string]string{"main/K": "sk-ant-api03-x"}); len(p) != 0 {
		t.Errorf("a key as a key: %v", p)
	}
	if p := checkCredentialKinds(credProject("anthropic-oauth", bearerService), map[string]string{"main/K": "sk-ant-oat01-x"}); len(p) != 0 {
		t.Errorf("a token as a token: %v", p)
	}
	other := config.ResolvedService{Hosts: []string{"api.deploy.test"}, Secret: "main/K", Inject: &config.Inject{Header: "x-api-key"}}
	if p := checkCredentialKinds(credProject("deploy", other), map[string]string{"main/K": "sk-ant-oat01-x"}); len(p) != 0 {
		t.Errorf("another host: %v", p)
	}
}

func TestBearerProviderSaysWhetherTheAnthropicCredentialIsABearerToken(t *testing.T) {
	if !bearerProvider(credProject("anthropic-oauth", bearerService), config.ResolvedAgent{Egress: "default"}) {
		t.Error("a bearer service on api.anthropic.com was not noticed")
	}
	if bearerProvider(credProject("anthropic", apiKeyService), config.ResolvedAgent{Egress: "default"}) {
		t.Error("an API key service counted as bearer")
	}
	if bearerProvider(credProject("anthropic", apiKeyService), config.ResolvedAgent{Egress: "missing"}) {
		t.Error("an unknown profile counted as bearer")
	}
}

func TestInspectingAWorktreeMountsItsBaseWhereItsGitFilePoints(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "coder")
	os.MkdirAll(worktree, 0o755)
	os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /.egzo/base/repo/.git/worktrees/coder\n"), 0o644)
	got, ok := worktreeBaseMount(worktree)
	want := MountSpec{Bind: true, Source: filepath.Join(root, ".base"), Target: "/.egzo/base/repo", ReadOnly: true}
	if !ok || got != want {
		t.Errorf("mount = %+v (%v), want %+v", got, ok, want)
	}
	for name, content := range map[string]string{
		"a clone has a .git directory, not a file": "",
		"a path outside the base":                  "gitdir: /etc/passwd\n",
		"a traversal":                              "gitdir: /.egzo/base/../../etc\n",
		"nothing after the prefix":                 "gitdir: /.egzo/base/\n",
		"not a gitdir line":                        "hello\n",
	} {
		other := filepath.Join(root, "x-"+strings.ReplaceAll(name, " ", "-"))
		os.MkdirAll(other, 0o755)
		if content != "" {
			os.WriteFile(filepath.Join(other, ".git"), []byte(content), 0o644)
		}
		if mount, ok := worktreeBaseMount(other); ok {
			t.Errorf("%s: got a mount %+v", name, mount)
		}
	}
}
