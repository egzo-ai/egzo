// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// WorkspacesEnv is the variable that describes an agent's own workspaces to it.
const WorkspacesEnv = "EGZO_WORKSPACE_INFO"

// Workspace is one workspace as the agent that lists it sees it. Nothing in it is about another agent or template: what an agent may know of
// the rest of the project is for the project owner to say in its prompt.
type Workspace struct {
	Name string `json:"name"`
	// Kind is git (a worktree of its own, on a branch of its own, over a base every agent that lists the workspace shares) or directory
	// (the same files for every agent that lists it).
	Kind string `json:"kind"`
	// Path is where the agent finds it: its own worktree for git.
	Path string `json:"path"`
	// Mode is rw or ro (a directory only: a git workspace is always rw).
	Mode string `json:"mode"`
	// Branch is the branch of the worktree, and From the branch of the repository it started from (empty: the repository's default).
	Branch string `json:"branch,omitempty"`
	From   string `json:"from,omitempty"`
}

// ParseWorkspaces reads the value of EGZO_WORKSPACE_INFO. An empty one is no workspaces.
func ParseWorkspaces(value string) ([]Workspace, error) {
	if value == "" {
		return nil, nil
	}
	var workspaces []Workspace
	if err := json.Unmarshal([]byte(value), &workspaces); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w", WorkspacesEnv, err)
	}
	return workspaces, nil
}

// workspacesInstructions is the paragraph for an agent with workspaces: where each one is, what kind it is, and what that means for working
// with other agents (their paths differ, and code goes from one to another through commits on a shared base).
func workspacesInstructions(workspaces []Workspace) string {
	var lines []string
	git := false
	for _, ws := range workspaces {
		switch ws.Kind {
		case "git":
			git = true
			from := "the repository's default branch"
			if ws.From != "" {
				from = ws.From
			}
			lines = append(lines, fmt.Sprintf("- %s: your own git worktree of the workspace %q, on the branch %s, started from %s. Nobody else works in it.",
				ws.Path, ws.Name, ws.Branch, from))
		default:
			access := "you can read and write it"
			if ws.Mode == "ro" {
				access = "it is read-only for you"
			}
			lines = append(lines, fmt.Sprintf("- %s: the directory workspace %q; %s. Every agent that lists %q sees the same files, live.",
				ws.Path, ws.Name, access, ws.Name))
		}
	}
	text := "Your workspaces:\n" + strings.Join(lines, "\n") + "\n"
	if git {
		text += "Other agents that list one of your git workspaces have a worktree of their own, which you cannot see, on a branch of their own (egzo/<their name>), " +
			"but you all share its base: its branches and commits. To hand code to another agent, commit it and send the branch and the commit; " +
			"it reads it with git show <commit>:<path> or git log <branch>, without pushing anything. Never run git worktree prune: the other agents' " +
			"worktrees only look missing from here, and pruning breaks them.\n"
	}
	text += "Another agent's paths are not yours: in a message, name a file by its workspace and its path inside it.\n\n"
	return text
}
