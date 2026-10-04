package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
)

func gitProject(t *testing.T, mode string) (*config.Resolved, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, ".egzo", "workspaces", "repo")
	mount := func(from string) config.Mount {
		return config.Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw", From: from}
	}
	reviewer := mount("coder")
	reviewer.Mount, reviewer.Mode = "/workspace/coder/repo", "ro"
	return &config.Resolved{
		Name: "proj",
		Workspaces: map[string]config.ResolvedWorkspace{
			"repo": {Git: &config.Git{URL: "https://github.com/acme/shop.git", Branch: "main"}, Mode: mode, Path: path},
		},
		Agents: map[string]config.ResolvedAgent{
			"coder":    {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount("")}},
			"tester":   {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount("")}},
			"reviewer": {Harness: "custom", Image: "x", Workdir: "/workspace", Workspaces: []config.Mount{reviewer}},
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
		t.Errorf("dirs = %v, want %v (a read-only reference makes no checkout of its own)", dirs, want)
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

func TestGitAgentsMountTheirCheckoutAndAReviewerMountsTheCodersReadOnly(t *testing.T) {
	project, path := gitProject(t, "clone")
	d, err := Desire(project, "/dir", Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "a", "tester": "b", "reviewer": "c"}})
	if err != nil {
		t.Fatal(err)
	}
	coder := findContainer(t, d, "proj-coder-1")
	if want := (MountSpec{Bind: true, Source: filepath.Join(path, "coder"), Target: "/workspace/repo"}); !slices.Contains(coder.Mounts, want) {
		t.Errorf("coder mounts = %+v", coder.Mounts)
	}
	reviewer := findContainer(t, d, "proj-reviewer-1")
	if want := (MountSpec{Bind: true, Source: filepath.Join(path, "coder"), Target: "/workspace/coder/repo", ReadOnly: true}); !slices.Contains(reviewer.Mounts, want) {
		t.Errorf("reviewer mounts = %+v", reviewer.Mounts)
	}
}

func TestWorktreeAgentsMountTheirWorktreeAndTheBaseButNeverAnotherAgents(t *testing.T) {
	project, path := gitProject(t, "worktree")
	d, _ := Desire(project, "/dir", Inputs{Image: "egzo:test", Tokens: map[string]string{}})
	coder := findContainer(t, d, "proj-coder-1")
	for _, want := range []MountSpec{
		{Bind: true, Source: filepath.Join(path, "coder"), Target: "/workspace/repo"},
		{Bind: true, Source: filepath.Join(path, ".base"), Target: "/.egzo/base/repo"},
	} {
		if !slices.Contains(coder.Mounts, want) {
			t.Errorf("coder lacks %+v: %+v", want, coder.Mounts)
		}
	}
	for _, m := range coder.Mounts {
		if strings.Contains(m.Source, "tester") || strings.Contains(m.Source, "reviewer") {
			t.Errorf("coder mounts another agent's directory: %+v", m)
		}
	}
}

func TestGitDirsListEveryCheckoutAWorkspaceCanHave(t *testing.T) {
	project, path := gitProject(t, "worktree")
	got := GitDirs(project)
	for _, want := range []string{filepath.Join(path, "coder"), filepath.Join(path, "tester"), filepath.Join(path, ".base")} {
		if !slices.Contains(got, want) {
			t.Errorf("GitDirs lacks %s: %v", want, got)
		}
	}
}

func TestUnsavedWorkIsDescribedWithItsDirectory(t *testing.T) {
	text := Unsaved{Dir: "/a/b", Uncommitted: 2, Unpushed: 1}.String()
	if !strings.Contains(text, "/a/b") || !strings.Contains(text, "2 uncommitted") || !strings.Contains(text, "1 unpushed") {
		t.Errorf("text = %q", text)
	}
}

func TestSplitAgentContainersKeepsTheSidecarsAndNetworksFirst(t *testing.T) {
	project, _ := gitProject(t, "clone")
	plan := []Action{
		{Verb: "create", Type: "network", Name: "proj_coder"},
		{Verb: "create", Type: "container", Name: "proj-proxy-1"},
		{Verb: "create", Type: "container", Name: "proj-coder-1"},
		{Verb: "start", Type: "container", Name: "proj-tester-1"},
		{Verb: "connect", Type: "network", Name: "proj_coder", Peer: "proj-proxy-1"},
	}
	before, agents := splitAgentContainers(plan, project)
	if len(before) != 3 || len(agents) != 2 || agents[0].Name != "proj-coder-1" {
		t.Errorf("before = %+v agents = %+v", before, agents)
	}
}
