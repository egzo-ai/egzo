// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

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
	// Whatever a repository carries in its own configuration is not trusted: a hook or a file-system
	// monitor there would run as whoever prepares or inspects it. A checkout an agent could write is
	// exactly such a repository.
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
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

// Clone clones url into dir (which must be empty or not exist), at branch when one is given. Nothing is
// checked out: the base clone holds no working files, so no branch is checked out in it and every worktree
// is free to take any branch.
func Clone(url, branch, dir string) error {
	if err := requireEmpty(dir); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--no-checkout"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dir)
	_, err := Git("", args...)
	return err
}

// Fetch brings the configured branch into the base under a ref of its own (see Worktree), leaving the base's own
// branches and remote as they are.
func Fetch(url, branch, base string) error {
	refspec := "+HEAD:" + fetched
	if branch != "" {
		refspec = "+refs/heads/" + branch + ":" + fetched
	}
	_, err := Git(base, "fetch", "--quiet", "--no-tags", "--", url, refspec)
	return err
}

// fetched is where Worktree keeps what it fetched: a ref of its own, so a base that is somebody's own
// clone keeps its branches and its remote as they are.
const fetched = "refs/egzo/fetched"

// WorktreeOptions says what Worktree makes.
type WorktreeOptions struct {
	URL, Branch string
	Base, Dir   string
	Agent       string
	NoFetch     bool // skip the fetch: the worktree starts from the base as it is
	Reuse       bool // continue an existing branch or an existing worktree instead of refusing it
}

// Branch is the branch of a worktree: one per instance, never checked out in two places.
func Branch(agent string) string { return "egzo/" + agent }

// Worktree makes Dir a worktree of the base clone in Base, on a branch of its own that starts at the head of
// the configured branch. It clones the base first when it is not there, and fetches first unless told not
// to; a fetch that fails is a warning, and the worktree starts from the base as it is. The links between the
// worktree and the base are relative, so they resolve wherever the two sit side by side.
func Worktree(o WorktreeOptions) (warnings []string, err error) {
	if _, statErr := os.Stat(filepath.Join(o.Base, ".git")); errors.Is(statErr, os.ErrNotExist) {
		if err := Clone(o.URL, o.Branch, o.Base); err != nil {
			return nil, err
		}
	} else if !o.NoFetch {
		refspec := "+HEAD:" + fetched
		if o.Branch != "" {
			refspec = "+refs/heads/" + o.Branch + ":" + fetched
		}
		// The URL of the file, not the remote of the base: the base may be someone's own clone, whose
		// remote may be one the proxy cannot serve.
		if _, fetchErr := Git(o.Base, "fetch", "--quiet", "--no-tags", "--", o.URL, refspec); fetchErr != nil {
			warnings = append(warnings, fmt.Sprintf("could not fetch %s: %s; the worktree starts from the base as it is", o.URL, firstLine(fetchErr.Error())))
		}
	}

	branch := Branch(o.Agent)
	if _, statErr := os.Stat(filepath.Join(o.Dir, ".git")); statErr == nil {
		if !o.Reuse {
			return warnings, fmt.Errorf("%s is a worktree already: use --reuse to continue with it", o.Dir)
		}
		return warnings, nil
	}
	if err := requireEmpty(o.Dir); err != nil {
		return warnings, err
	}
	_, existsErr := Git(o.Base, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	switch {
	case existsErr == nil && !o.Reuse:
		return warnings, fmt.Errorf("the branch %s already exists in the base clone: use --reuse to continue it", branch)
	case existsErr == nil:
		// --force: the base may still have this worktree registered at a path that is gone
		_, err = Git(o.Base, "worktree", "add", "--relative-paths", "--force", "--quiet", o.Dir, branch)
	default:
		_, err = Git(o.Base, "worktree", "add", "--relative-paths", "--quiet", "-b", branch, o.Dir, o.startPoint())
	}
	return warnings, err
}

// startPoint is the commit a new branch starts from: what was fetched, else what the clone saw.
func (o WorktreeOptions) startPoint() string {
	if _, err := Git(o.Base, "rev-parse", "--verify", "--quiet", fetched); err == nil {
		return fetched
	}
	if o.Branch != "" {
		return "refs/remotes/origin/" + o.Branch
	}
	return "refs/remotes/origin/HEAD"
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
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
