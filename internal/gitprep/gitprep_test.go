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

func TestCloneChecksOutTheBranch(t *testing.T) {
	url := origin(t)
	dir := filepath.Join(t.TempDir(), "clone")
	if err := Clone(url, "side", dir); err != nil {
		t.Fatal(err)
	}
	if branch, _ := Git(dir, "branch", "--show-current"); branch != "side" {
		t.Errorf("branch = %q", branch)
	}
	if remote, _ := Git(dir, "remote", "get-url", "origin"); remote != url {
		t.Errorf("origin = %q", remote)
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

func TestWorktreesShareOneBaseAndEachHasItsOwnBranch(t *testing.T) {
	url := origin(t)
	root := t.TempDir()
	base := filepath.Join(root, "base")
	one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
	if err := Worktree(url, "", base, one, "coder"); err != nil {
		t.Fatal(err)
	}
	if err := Worktree(url, "", base, two, "reviewer"); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{one: "egzo/coder", two: "egzo/reviewer"} {
		if branch, _ := Git(dir, "branch", "--show-current"); branch != want {
			t.Errorf("%s is on %q, want %q", dir, branch, want)
		}
	}
	if _, err := os.Stat(filepath.Join(one, "README")); err != nil {
		t.Error("the worktree has no files")
	}
	if list, _ := Git(base, "worktree", "list"); strings.Count(list, "\n") != 2 {
		t.Errorf("worktrees = %s", list)
	}
}

func TestInspectReportsUncommittedAndUnpushedWork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c")
	if err := Clone(origin(t), "", dir); err != nil {
		t.Fatal(err)
	}
	if findings, err := Inspect(dir); err != nil || !findings.Clean() {
		t.Fatalf("a fresh clone is not clean: %+v, %v", findings, err)
	}
	os.WriteFile(filepath.Join(dir, "wip"), []byte("x"), 0o644)
	if findings, _ := Inspect(dir); findings.Uncommitted != 1 || findings.Unpushed != 0 {
		t.Errorf("findings = %+v", findings)
	}
	Git(dir, "add", ".")
	Git(dir, "commit", "-qm", "local")
	if findings, _ := Inspect(dir); findings.Uncommitted != 0 || findings.Unpushed != 1 {
		t.Errorf("findings = %+v", findings)
	}
}

func TestASecondWorktreeMayUseAPathAnotherAgentsWorktreeIsRegisteredAt(t *testing.T) {
	// In the containers every agent sees its worktree at the same path, and in a prep container the
	// other agents' directories are not mounted, so git finds that path registered and missing.
	url := origin(t)
	root := t.TempDir()
	base := filepath.Join(root, "base")
	path := filepath.Join(root, "workspace")
	if err := Worktree(url, "", base, path, "coder"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := Worktree(url, "", base, path, "reviewer"); err != nil {
		t.Errorf("err = %v", err)
	}
	if branch, _ := Git(path, "branch", "--show-current"); branch != "egzo/reviewer" {
		t.Errorf("branch = %q", branch)
	}
}

func cloned(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "c")
	if err := Clone(origin(t), "", dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInspectSeesStashedWork(t *testing.T) {
	dir := cloned(t)
	os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644)
	if _, err := Git(dir, "stash"); err != nil {
		t.Fatal(err)
	}
	findings, err := Inspect(dir)
	if err != nil || findings.Stashes != 1 || findings.Uncommitted != 0 || findings.Clean() {
		t.Errorf("findings = %+v, %v: a stash is work that exists nowhere else", findings, err)
	}
}

func TestInspectSeesCommitsOnADetachedHead(t *testing.T) {
	dir := cloned(t)
	Git(dir, "checkout", "-q", "--detach")
	if _, err := Git(dir, "commit", "-q", "--allow-empty", "-m", "on no branch"); err != nil {
		t.Fatal(err)
	}
	if findings, _ := Inspect(dir); findings.Unpushed != 1 {
		t.Errorf("findings = %+v: a commit on no branch is not on any remote either", findings)
	}
	Git(dir, "checkout", "-q", "main")
	if findings, _ := Inspect(dir); findings.Unpushed != 1 {
		t.Logf("after leaving the detached head its commit is unreachable but still counted by reflog only: %+v", findings)
	}
}

func TestInspectCountsIgnoredFilesSeparately(t *testing.T) {
	dir := cloned(t)
	os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("*.secret\nbuild/\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.secret"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "build"), 0o755)
	os.WriteFile(filepath.Join(dir, "build", "out"), []byte("x"), 0o644)
	findings, err := Inspect(dir)
	if err != nil || findings.Ignored != 2 || findings.Uncommitted != 0 || !findings.Clean() {
		t.Errorf("findings = %+v, %v: ignored files are reported but are not unsaved work", findings, err)
	}
}

func TestAHookInTheRepositoryIsNeverRun(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	if err := Clone(origin(t), "", base); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(base, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	if err := Worktree("unused", "", base, filepath.Join(t.TempDir(), "wt"), "coder"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook planted in the base repository ran while a worktree was prepared")
	}
}

func TestAFileSystemMonitorInTheRepositoryConfigIsNeverRun(t *testing.T) {
	dir := cloned(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "monitor.sh")
	os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	exec.Command("git", "-C", dir, "config", "core.fsmonitor", script).Run()
	Inspect(dir)
	if _, err := os.Stat(marker); err == nil {
		t.Error("core.fsmonitor from the repository's own config ran during the inspection")
	}
}
