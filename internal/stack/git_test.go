// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

func gitProject(t *testing.T) (*config.Resolved, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspaces", "repo")
	mount := config.Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}
	return &config.Resolved{
		Name: "proj",
		Workspaces: map[string]config.ResolvedWorkspace{
			"repo": {Git: &config.Git{URL: "https://github.com/acme/shop.git", Branch: "main"}, Path: path},
			"data": {Path: filepath.Join(filepath.Dir(path), "data")},
		},
		Agents: map[string]config.ResolvedAgent{
			"coder":  {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"tester": {Harness: "custom", Image: "x", Workdir: "/workspace/repo", Workspaces: []config.Mount{mount}},
			"docs":   {Harness: "custom", Image: "x", Workdir: "/workspace", Workspaces: []config.Mount{{Name: "data", Mount: "/workspace/data", Mode: "rw", HostPath: filepath.Join(filepath.Dir(path), "data")}}},
		},
	}, path
}

func TestEveryInstanceThatListsAGitWorkspaceGetsAWorktreeNamedAfterIt(t *testing.T) {
	project, path := gitProject(t)
	plan := PlanWorktrees(project, "coder")
	if len(plan) != 1 || plan[0].Dir != filepath.Join(path, "coder") || plan[0].Base != filepath.Join(path, ".base") ||
		plan[0].URL != "https://github.com/acme/shop.git" || plan[0].Branch != "main" || plan[0].Workspace != "repo" {
		t.Errorf("plan = %+v", plan)
	}
	if got := PlanWorktrees(project, "docs"); len(got) != 0 {
		t.Errorf("an instance with only a directory workspace needs no worktree: %+v", got)
	}
}

func TestAWorktreeOrABranchThatIsThereRefusesTheSpawnUnlessReused(t *testing.T) {
	project, path := gitProject(t)
	plan := PlanWorktrees(project, "coder")
	if err := CheckWorktrees(plan, false); err != nil {
		t.Fatalf("nothing is there yet: %v", err)
	}
	os.MkdirAll(filepath.Join(path, "coder"), 0o755)
	if err := CheckWorktrees(plan, false); err == nil || !strings.Contains(err.Error(), "--reuse") || !strings.Contains(err.Error(), filepath.Join(path, "coder")) {
		t.Errorf("a worktree that exists: %v", err)
	}
	if err := CheckWorktrees(plan, true); err != nil {
		t.Errorf("reuse: %v", err)
	}
	os.Remove(filepath.Join(path, "coder"))
	loose := filepath.Join(path, ".base", ".git", "refs", "heads", "egzo")
	os.MkdirAll(loose, 0o755)
	os.WriteFile(filepath.Join(loose, "coder"), []byte("abc\n"), 0o644)
	if err := CheckWorktrees(plan, false); err == nil || !strings.Contains(err.Error(), "egzo/coder") || !strings.Contains(err.Error(), "--reuse") {
		t.Errorf("a loose branch: %v", err)
	}
	os.Remove(filepath.Join(loose, "coder"))
	os.WriteFile(filepath.Join(path, ".base", ".git", "packed-refs"), []byte("# pack-refs\nabc refs/heads/egzo/coder\n"), 0o644)
	if err := CheckWorktrees(plan, false); err == nil {
		t.Error("a packed branch was not seen")
	}
}

func TestAnInstanceMountsItsOwnWorktreeAndTheBaseUnderTheNamesTheyHaveOnTheHost(t *testing.T) {
	project, path := gitProject(t)
	spec, err := agentContainer(project, "/p", "coder", project.Agents["coder"], "proj_coder", Inputs{}, engine.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	want := []MountSpec{
		{Bind: true, Source: filepath.Join(path, "coder"), Target: "/workspace/repo/coder"},
		{Bind: true, Source: filepath.Join(path, ".base"), Target: "/workspace/repo/.base"},
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
		if strings.Contains(m.Source, "tester") {
			t.Errorf("another instance's worktree is mounted: %+v", m)
		}
	}
}

func TestTheWorkingDirectoryMovesIntoTheWorktreeOnlyForGitWorkspaces(t *testing.T) {
	project, _ := gitProject(t)
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

func TestDirectoryWorkspacesAreCreatedEmptyAndLeftAloneWhenTheyExist(t *testing.T) {
	project, path := gitProject(t)
	data := filepath.Join(filepath.Dir(path), "data")
	if err := EnsureDirectories(project); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(data); err != nil || !info.IsDir() {
		t.Fatalf("the directory was not made: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a git workspace's directory was made by EnsureDirectories: `up` clones it")
	}
	os.WriteFile(filepath.Join(data, "mine"), []byte("x"), 0o644)
	if err := EnsureDirectories(project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "mine")); err != nil {
		t.Error("an existing directory was touched")
	}
}

func TestBaseURLIsReadFromTheOriginOfTheBase(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, ".git"), 0o755)
	config := "[core]\n\turl = not-this\n[remote \"upstream\"]\n\turl = https://x/upstream.git\n[remote \"origin\"]\n\turl = https://x/y.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	os.WriteFile(filepath.Join(base, ".git", "config"), []byte(config), 0o644)
	if got := baseURL(base); got != "https://x/y.git" {
		t.Errorf("url = %q", got)
	}
	if got := baseURL(t.TempDir()); got != "" {
		t.Errorf("a directory with no repository has the url %q", got)
	}
}

func TestRemovingWorktreesDeletesOnlyThoseOfTheInstancesAndWhatTheBaseKeepsAboutThem(t *testing.T) {
	project, path := gitProject(t)
	base := filepath.Join(path, ".base")
	for _, instance := range []string{"coder", "tester"} {
		os.MkdirAll(filepath.Join(path, instance), 0o755)
		os.WriteFile(filepath.Join(path, instance, "work.txt"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(path, instance, ".git"), []byte("gitdir: ../.base/.git/worktrees/"+instance+"\n"), 0o644)
		meta := filepath.Join(base, ".git", "worktrees", instance)
		os.MkdirAll(meta, 0o755)
		os.WriteFile(filepath.Join(meta, "gitdir"), []byte("../../../../"+instance+"/.git\n"), 0o644)
	}
	notAWorktree := filepath.Join(path, "mine")
	os.MkdirAll(notAWorktree, 0o755)
	os.WriteFile(filepath.Join(notAWorktree, "precious"), []byte("x"), 0o644)

	if err := RemoveWorktrees(project, []string{"coder", "mine", "ghost"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for path, wantGone := range map[string]bool{
		filepath.Join(path, "coder"):                       true,
		filepath.Join(base, ".git", "worktrees", "coder"):  true,
		filepath.Join(path, "tester"):                      false,
		filepath.Join(base, ".git", "worktrees", "tester"): false,
		filepath.Join(notAWorktree, "precious"):            false, // no .git in it: not something egzo made
		base:                                               false,
	} {
		if _, err := os.Stat(path); (err != nil) != wantGone {
			t.Errorf("%s: gone = %v, want %v", path, err != nil, wantGone)
		}
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
