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

// BaseInfo is what a base clone says about itself, for the CLI to decide with: the CLI cannot look inside the
// volume that holds it.
type BaseInfo struct {
	// Cloned is whether the directory holds a clone at all.
	Cloned bool `json:"cloned"`
	// URL is the url of the clone's origin, "" when it has none.
	URL string `json:"url"`
	// Branches are the branches egzo made in it, one per instance that was ever made (egzo/<instance>).
	Branches []string `json:"branches"`
}

// Info reads the base clone in base. A directory with no clone is not an error: it says so.
func Info(base string) (BaseInfo, error) {
	info := BaseInfo{Branches: []string{}}
	if _, err := os.Stat(filepath.Join(base, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return info, nil
		}
		return info, err
	}
	info.Cloned = true
	if url, err := Git(base, "config", "--get", "remote.origin.url"); err == nil {
		info.URL = url
	}
	listed, err := Git(base, "for-each-ref", "--format=%(refname)", "refs/heads/egzo/")
	if err != nil {
		return info, err
	}
	for _, ref := range strings.Split(listed, "\n") {
		if name := strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"); name != "" {
			info.Branches = append(info.Branches, name)
		}
	}
	return info, nil
}

// Forget removes what the base keeps about the worktrees of the given instances, which sit next to it and are
// gone: git would refuse to make a worktree of the same name while it remembers one. Only those worktrees are
// forgotten: the base cannot see the others (each is in a volume of its own) and a prune would take them for gone.
func Forget(base string, instances []string) error {
	entries, err := os.ReadDir(filepath.Join(base, ".git", "worktrees"))
	if err != nil {
		return nil // nothing is remembered
	}
	gone := map[string]bool{}
	for _, instance := range instances {
		gone[filepath.Join(filepath.Dir(base), instance, ".git")] = true
	}
	var failed []error
	for _, entry := range entries {
		meta := filepath.Join(base, ".git", "worktrees", entry.Name())
		data, err := os.ReadFile(filepath.Join(meta, "gitdir"))
		if err != nil {
			continue
		}
		target := strings.TrimSpace(string(data))
		if !filepath.IsAbs(target) {
			target = filepath.Join(meta, target)
		}
		if gone[filepath.Clean(target)] {
			if err := os.RemoveAll(meta); err != nil {
				failed = append(failed, err)
			}
		}
	}
	return errors.Join(failed...)
}
