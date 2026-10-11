// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package gitprep

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// origin makes a bare repository with one commit on main and one on a second branch.
func origin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "origin.git")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", work},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(work, "README"), []byte("hello\n"), 0o644)
	for _, args := range [][]string{
		{"add", "."}, {"commit", "-qm", "first"}, {"checkout", "-qb", "side"},
		{"commit", "-q", "--allow-empty", "-m", "on side"}, {"checkout", "-q", "main"},
	} {
		if _, err := Git(work, args...); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return bare
}

func TestCloneChecksNothingOut(t *testing.T) {
	url := origin(t)
	dir := filepath.Join(t.TempDir(), "clone")
	if err := Clone(url, "side", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README")); err == nil {
		t.Error("the base clone has working files")
	}
	if remote, _ := Git(dir, "remote", "get-url", "origin"); remote != url {
		t.Errorf("origin = %q", remote)
	}
	if _, err := Git(dir, "rev-parse", "--verify", "refs/remotes/origin/side"); err != nil {
		t.Errorf("the branch asked for is not there: %v", err)
	}
}

func TestCloneRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine"), []byte("x"), 0o644)
	if err := Clone(origin(t), "", dir); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mine")); err != nil {
		t.Error("an existing file was touched")
	}
}

func TestCloneFailureSaysWhy(t *testing.T) {
	err := Clone(filepath.Join(t.TempDir(), "nowhere.git"), "", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "nowhere.git") {
		t.Errorf("err = %v", err)
	}
}

// layout is how a git workspace sits on the host: the base and the worktrees side by side.
func layout(t *testing.T) (root, base string) {
	root = t.TempDir()
	return root, filepath.Join(root, ".base")
}

func worktree(t *testing.T, url, branch, base, root, agent string, mutate ...func(*WorktreeOptions)) ([]string, error) {
	options := WorktreeOptions{URL: url, Branch: branch, Base: base, Dir: filepath.Join(root, agent), Agent: agent}
	for _, m := range mutate {
		m(&options)
	}
	return Worktree(options)
}

func TestWorktreesShareOneBaseAndEachHasItsOwnBranch(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	for _, agent := range []string{"coder", "reviewer"} {
		if _, err := worktree(t, url, "", base, root, agent); err != nil {
			t.Fatal(err)
		}
	}
	for agent, want := range map[string]string{"coder": "egzo/coder", "reviewer": "egzo/reviewer"} {
		if branch, _ := Git(filepath.Join(root, agent), "branch", "--show-current"); branch != want {
			t.Errorf("%s is on %q, want %q", agent, branch, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "coder", "README")); err != nil {
		t.Error("the worktree has no files")
	}
	if _, err := os.Stat(filepath.Join(base, "README")); err == nil {
		t.Error("the base has working files")
	}
	if list, _ := Git(base, "worktree", "list"); strings.Count(list, "\n") != 2 {
		t.Errorf("worktrees = %s", list)
	}
}

func TestTheWorktreeLinksAreRelative(t *testing.T) {
	root, base := layout(t)
	if _, err := worktree(t, origin(t), "", base, root, "coder"); err != nil {
		t.Fatal(err)
	}
	link, _ := os.ReadFile(filepath.Join(root, "coder", ".git"))
	if got := strings.TrimSpace(string(link)); got != "gitdir: ../.base/.git/worktrees/coder" {
		t.Errorf("worktree link = %q", got)
	}
	back, _ := os.ReadFile(filepath.Join(base, ".git", "worktrees", "coder", "gitdir"))
	if got := strings.TrimSpace(string(back)); got != "../../../../coder/.git" {
		t.Errorf("base link = %q", got)
	}
}

func TestTheWorktreeStartsAtTheConfiguredBranch(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	if _, err := worktree(t, url, "side", base, root, "coder"); err != nil {
		t.Fatal(err)
	}
	if subject, _ := Git(filepath.Join(root, "coder"), "log", "-1", "--format=%s"); subject != "on side" {
		t.Errorf("the worktree is at %q", subject)
	}
}

func TestAFetchBringsInWhatWasPushedAfterTheClone(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	if _, err := worktree(t, url, "", base, root, "first"); err != nil {
		t.Fatal(err)
	}
	pusher := filepath.Join(t.TempDir(), "pusher")
	if out, err := exec.Command("git", "clone", "-q", url, pusher).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, args := range [][]string{{"commit", "-q", "--allow-empty", "-m", "newer"}, {"push", "-q", "origin", "main"}} {
		if _, err := Git(pusher, append([]string{"-c", "user.name=a", "-c", "user.email=a@b.c"}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := worktree(t, url, "", base, root, "second"); err != nil {
		t.Fatal(err)
	}
	if subject, _ := Git(filepath.Join(root, "second"), "log", "-1", "--format=%s"); subject != "newer" {
		t.Errorf("the worktree is at %q, want the commit pushed after the clone", subject)
	}
	if _, err := worktree(t, url, "", base, root, "third", func(o *WorktreeOptions) { o.NoFetch = true }); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedFetchIsAWarningAndTheWorktreeIsMadeFromTheBase(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	if _, err := worktree(t, url, "", base, root, "first"); err != nil {
		t.Fatal(err)
	}
	warnings, err := worktree(t, filepath.Join(t.TempDir(), "gone.git"), "", base, root, "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not fetch") || !strings.Contains(warnings[0], "gone.git") {
		t.Errorf("warnings = %q", warnings)
	}
	if _, err := os.Stat(filepath.Join(root, "second", "README")); err != nil {
		t.Error("the worktree has no files")
	}
}

func TestTheFetchUsesTheGivenURLAndNotTheRemoteOfTheBase(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	if _, err := worktree(t, url, "", base, root, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(base, "remote", "set-url", "origin", "ssh://nowhere.invalid/x.git"); err != nil {
		t.Fatal(err)
	}
	warnings, err := worktree(t, url, "", base, root, "second")
	if err != nil || len(warnings) != 0 {
		t.Errorf("warnings = %q, err = %v", warnings, err)
	}
}

func TestAnExistingBranchOrWorktreeIsRefusedUnlessReused(t *testing.T) {
	url := origin(t)
	root, base := layout(t)
	if _, err := worktree(t, url, "", base, root, "coder"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree(t, url, "", base, root, "coder"); err == nil || !strings.Contains(err.Error(), "--reuse") {
		t.Errorf("a worktree that exists was accepted: %v", err)
	}
	if _, err := worktree(t, url, "", base, root, "coder", func(o *WorktreeOptions) { o.Reuse = true }); err != nil {
		t.Errorf("reuse of a worktree that exists: %v", err)
	}
	// the worktree goes, its branch stays
	if err := os.RemoveAll(filepath.Join(root, "coder")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree(t, url, "", base, root, "coder"); err == nil || !strings.Contains(err.Error(), "egzo/coder") || !strings.Contains(err.Error(), "--reuse") {
		t.Errorf("a branch that exists was accepted: %v", err)
	}
	if _, err := worktree(t, url, "", base, root, "coder", func(o *WorktreeOptions) { o.Reuse = true }); err != nil {
		t.Errorf("reuse of a branch that exists: %v", err)
	}
	if branch, _ := Git(filepath.Join(root, "coder"), "branch", "--show-current"); branch != "egzo/coder" {
		t.Errorf("branch = %q", branch)
	}
}

func TestAHookInTheRepositoryIsNeverRun(t *testing.T) {
	root, base := layout(t)
	url := origin(t)
	if err := Clone(url, "", base); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(base, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	if _, err := worktree(t, url, "", base, root, "coder", func(o *WorktreeOptions) { o.NoFetch = true }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook planted in the base repository ran while a worktree was prepared")
	}
}
