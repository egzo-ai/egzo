// Package gitprep is what the prep container runs: the git work behind git workspaces. The CLI
// decides where checkouts go and which agent needs which; this package only does the git.
package gitprep

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git runs git in dir with a clean environment: no terminal prompts, no user or system
// configuration, so the result depends on nothing but its arguments.
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=egzo", "GIT_AUTHOR_EMAIL=egzo@localhost",
		"GIT_COMMITTER_NAME=egzo", "GIT_COMMITTER_EMAIL=egzo@localhost",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		return text, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// Clone clones url into dir (which must be empty or not exist), at branch when one is given.
func Clone(url, branch, dir string) error {
	if err := requireEmpty(dir); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dir)
	_, err := Git("", args...)
	return err
}

// Worktree makes dir a worktree of the base clone in base, cloning the base first when it is not
// there. Each worktree gets a branch of its own, because git does not check one branch out twice.
func Worktree(url, branch, base, dir, agent string) error {
	if _, err := os.Stat(filepath.Join(base, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := Clone(url, branch, base); err != nil {
			return err
		}
	}
	if err := requireEmpty(dir); err != nil {
		return err
	}
	start := branch
	if start == "" {
		start = "HEAD"
	}
	// --force: every agent sees its worktree at the same path (/workspace/<name>), so the base
	// already has another agent's worktree registered there; git calls that "missing" because that
	// agent's directory is not mounted in this container.
	_, err := Git(base, "worktree", "add", "--force", "--quiet", "-b", "egzo/"+agent, dir, start)
	return err
}

func requireEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	return nil
}

// Findings are the reasons a checkout must not be thrown away.
type Findings struct {
	Uncommitted int // files with changes, staged or not, and untracked files
	Unpushed    int // commits on a local branch that no remote branch has
}

func (f Findings) Clean() bool { return f.Uncommitted == 0 && f.Unpushed == 0 }

// Inspect looks for work in dir that exists nowhere else.
func Inspect(dir string) (Findings, error) {
	var findings Findings
	status, err := Git(dir, "status", "--porcelain")
	if err != nil {
		return findings, err
	}
	if status != "" {
		findings.Uncommitted = len(strings.Split(status, "\n"))
	}
	unpushed, err := Git(dir, "log", "--branches", "--not", "--remotes", "--oneline")
	if err != nil {
		return findings, err
	}
	if unpushed != "" {
		findings.Unpushed = len(strings.Split(unpushed, "\n"))
	}
	return findings, nil
}
