// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

func gitProject() *config.Resolved {
	mount := config.Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}
	return &config.Resolved{
		Name: "proj",
		Workspaces: map[string]config.ResolvedWorkspace{
			"repo": {Git: &config.Git{URL: "https://github.com/acme/shop.git", Branch: "main"}},
			"data": {},
		},
		Agents: map[string]config.ResolvedAgent{
			"coder":  {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"tester": {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"docs":   {Harness: "custom", Image: "x", Workdir: "/workspace", Workspaces: []config.Mount{{Name: "data", Mount: "/workspace/data", Mode: "rw"}}},
		},
	}
}

func TestWorkspaceVolumeNamesCannotCollideWithAnyOtherVolumeOfTheProject(t *testing.T) {
	if got := WorkspaceVolume("proj", "data"); got != "proj_ws.data" {
		t.Errorf("directory volume = %q", got)
	}
	if got := BaseVolume("proj", "repo"); got != "proj_ws.repo.base" {
		t.Errorf("base volume = %q", got)
	}
	if got := WorktreeVolume("proj", "repo", "coder-1"); got != "proj_ws.repo.wt.coder-1" {
		t.Errorf("worktree volume = %q", got)
	}
	// an instance named base, or a workspace named like an agent's home, cannot reach another volume's name
	if WorktreeVolume("proj", "repo", "base") == BaseVolume("proj", "repo") {
		t.Error("a worktree of an instance called base is the base")
	}
	for _, other := range []string{"proj_control", "proj_ca", "proj_ca-private", "proj_egress", "proj_coder-home", "proj_x-home"} {
		for _, ours := range []string{WorkspaceVolume("proj", "x-home"), WorkspaceVolume("proj", "control"), BaseVolume("proj", "ca"), WorktreeVolume("proj", "ca", "private")} {
			if other == ours {
				t.Errorf("%s collides", other)
			}
		}
	}
}

func TestEveryInstanceThatListsAGitWorkspaceGetsAWorktreeVolumeNamedAfterIt(t *testing.T) {
	project := gitProject()
	plan := PlanWorktrees(project, "coder")
	if len(plan) != 1 || plan[0].Volume != "proj_ws.repo.wt.coder" || plan[0].Base != "proj_ws.repo.base" ||
		plan[0].URL != "https://github.com/acme/shop.git" || plan[0].Branch != "main" || plan[0].Workspace != "repo" {
		t.Errorf("plan = %+v", plan)
	}
	if got := PlanWorktrees(project, "docs"); len(got) != 0 {
		t.Errorf("an instance with only a directory workspace needs no worktree: %+v", got)
	}
}

func TestAnInstanceMountsItsOwnWorktreeAndTheBaseAsSiblings(t *testing.T) {
	project := gitProject()
	spec, err := agentContainer(project, "/p", "coder", project.Agents["coder"], "proj_coder", Inputs{}, engine.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	want := []MountSpec{
		{Source: "proj_ws.repo.wt.coder", Target: "/workspace/repo/coder"},
		{Source: "proj_ws.repo.base", Target: "/workspace/repo/.base"},
	}
	var got []MountSpec
	for _, m := range spec.Mounts {
		if strings.HasPrefix(m.Target, "/workspace/") {
			got = append(got, m)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("mounts = %+v, want %+v", got, want)
	}
	if spec.WorkingDir != "/workspace/repo/coder" {
		t.Errorf("working directory = %q", spec.WorkingDir)
	}
	for _, m := range got {
		if strings.Contains(m.Source, "tester") || m.Bind {
			t.Errorf("another instance's worktree, or the host, is mounted: %+v", m)
		}
	}
	if !slices.Contains(spec.OwnedVolumes, "proj_ws.repo.wt.coder") || slices.Contains(spec.OwnedVolumes, "proj_ws.repo.base") {
		t.Errorf("owned volumes = %v: the worktree is handed to the agent user, the shared base is not walked at every spawn", spec.OwnedVolumes)
	}
}

func TestTheWorkingDirectoryMovesIntoTheWorktreeOnlyForGitWorkspaces(t *testing.T) {
	project := gitProject()
	agent := project.Agents["coder"]
	for workdir, want := range map[string]string{
		"/workspace/repo":       "/workspace/repo/coder",
		"/workspace/repo/src":   "/workspace/repo/coder/src",
		"/workspace":            "/workspace",
		"/workspace/repository": "/workspace/repository",
	} {
		agent.Workdir = workdir
		if got := instanceWorkdir(project, agent, "coder"); got != want {
			t.Errorf("workdir %q became %q, want %q", workdir, got, want)
		}
	}
	docs := project.Agents["docs"]
	docs.Workdir = "/workspace/data"
	if got := instanceWorkdir(project, docs, "docs"); got != "/workspace/data" {
		t.Errorf("a directory workspace's working directory moved: %q", got)
	}
}

func TestDirectoryWorkspacesOfAnAgentAreTheOnesWithoutGit(t *testing.T) {
	project := gitProject()
	if got := DirectoryWorkspacesOf(project, project.Agents["docs"]); !slices.Equal(got, []string{"data"}) {
		t.Errorf("docs = %v", got)
	}
	if got := DirectoryWorkspacesOf(project, project.Agents["coder"]); len(got) != 0 {
		t.Errorf("coder = %v: a git workspace is not a directory", got)
	}
}

func TestWorkspaceRowsSayWhatTheEngineHolds(t *testing.T) {
	project := gitProject()
	observed := Observed{Resources: []Resource{
		{Type: "volume", Name: "proj_ws.repo.base", Kind: kindWorkspace, Workspace: "repo"},
		{Type: "volume", Name: "proj_ws.repo.wt.b", Kind: kindWorkspace, Workspace: "repo", Instance: "b"},
		{Type: "volume", Name: "proj_ws.repo.wt.a", Kind: kindWorkspace, Workspace: "repo", Instance: "a"},
	}}
	rows := WorkspaceRows(project, observed)
	if len(rows) != 2 || rows[0].Name != "data" || rows[1].Name != "repo" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].State != "missing" || rows[0].Kind != "directory" || rows[0].Volume != "proj_ws.data" {
		t.Errorf("data = %+v", rows[0])
	}
	if rows[1].State != "cloned" || rows[1].Kind != "git" || !slices.Equal(rows[1].Worktrees, []string{"a", "b"}) {
		t.Errorf("repo = %+v", rows[1])
	}
	if rows := WorkspaceRows(project, Observed{}); rows[1].State != "not cloned" {
		t.Errorf("with nothing observed: %+v", rows[1])
	}
}

func TestOnlyBaseVolumesAreBasesAndDownKeepsOnlyWhatIsNotAnInstancesOwn(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "volume", Name: "proj_ws.repo.base", Kind: kindWorkspace, Workspace: "repo"},
		{Type: "volume", Name: "proj_ws.data", Kind: kindWorkspace, Workspace: "data"},
		{Type: "volume", Name: "proj_ws.repo.wt.a", Kind: kindWorkspace, Workspace: "repo", Instance: "a"},
		{Type: "container", Name: "proj-a", Kind: kindAgent, Instance: "a", State: "running"},
		{Type: "volume", Name: "proj_ws.repo.wt.gone", Kind: kindWorkspace, Workspace: "repo", Instance: "gone"},
		{Type: "volume", Name: "proj_a-home", Kind: kindAgent, Instance: "a"},
		{Type: "volume", Name: "proj_control", Kind: kindControl},
	}}
	if got := baseWorkspaces(observed); !slices.Equal(got, []string{"repo"}) {
		t.Errorf("bases = %v", got)
	}
	kept := []string{}
	for _, r := range observed.Resources {
		if keptByDown(r, orphanWorktrees(observed)) {
			kept = append(kept, r.Name)
		}
	}
	if !slices.Equal(kept, []string{"proj_ws.repo.base", "proj_ws.data", "proj_ws.repo.wt.gone"}) {
		t.Errorf("down keeps %v: the bases, the directories, and a worktree no instance owns", kept)
	}
}

func TestPathsInsideAWorkspaceNeverLeaveIt(t *testing.T) {
	for in, want := range map[string]string{"a.txt": "a.txt", "./a/../b": "b", "a/b/": "a/b", ".": "", "": ""} {
		if got, err := InsidePath(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"..", "../x", "/etc/passwd", "a/../../x"} {
		if _, err := InsidePath(bad); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}
