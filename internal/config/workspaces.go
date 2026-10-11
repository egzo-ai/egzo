// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"path/filepath"
	"sort"
	"strings"
)

const workspaceRoot = "/workspace"

// ResolvedWorkspace is a declared workspace. A workspace is never on the host: it is an engine volume, an empty
// directory without Git, or with Git a base clone and one worktree per instance, each in a volume of its own.
type ResolvedWorkspace struct {
	Git *Git `yaml:"git,omitempty"`
}

// Mount is one workspace as an agent sees it.
type Mount struct {
	Name  string `yaml:"name"`
	Mount string `yaml:"mount"`
	Mode  string `yaml:"mode"`
}

func resolveWorkspaces(file *File, p *problems) map[string]ResolvedWorkspace {
	resolved := map[string]ResolvedWorkspace{}
	for _, name := range sortedWorkspaceNames(file) {
		workspace := file.Workspaces[name]
		if !identifier.MatchString(name) {
			continue // checkNames reports it
		}
		if workspace.Git != nil {
			checkGit(name, workspace.Git, p)
		}
		resolved[name] = ResolvedWorkspace(workspace)
	}
	return resolved
}

func sortedWorkspaceNames(file *File) []string {
	names := make([]string, 0, len(file.Workspaces))
	for name := range file.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ref is a parsed entry of an agent's workspaces list: name[:ro].
type ref struct {
	raw      string
	target   string
	readOnly bool
}

func parseRef(raw string) (ref, bool) {
	parsed := ref{raw: raw}
	rest := raw
	switch {
	case strings.HasSuffix(rest, ":ro"):
		parsed.readOnly = true
		rest = strings.TrimSuffix(rest, ":ro")
	case strings.HasSuffix(rest, ":rw"):
		rest = strings.TrimSuffix(rest, ":rw")
	}
	if rest == "" || strings.ContainsAny(rest, ":/") {
		return parsed, false
	}
	parsed.target = rest
	return parsed, true
}

// agentRefs parses every agent's workspaces list once.
func agentRefs(file *File, p *problems) map[string][]ref {
	refs := map[string][]ref{}
	for name, agent := range file.Agents {
		for _, raw := range agent.Workspaces {
			parsed, ok := parseRef(raw)
			if !ok {
				if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "/") {
					p.addf("agent %q: workspace %q is a path, and an agent lists only declared workspaces: declare it once under workspaces: "+
						"(for example `docs: {path: %s}`) and list its name", name, raw, strings.TrimSuffix(strings.TrimSuffix(raw, ":ro"), ":rw"))
				} else {
					p.addf("agent %q: unsupported workspace reference %q (use name or name:ro; a template cannot mount another agent's worktree)", name, raw)
				}
				continue
			}
			refs[name] = append(refs[name], parsed)
		}
	}
	return refs
}

// resolveMounts turns an agent's references into mounts and its working directory. A directory workspace is one
// volume; a git workspace is mounted per instance (its worktree and the base), which is decided when the
// instance is made.
func resolveMounts(
	name string, agent Agent, workspaces map[string]ResolvedWorkspace, refs map[string][]ref, p *problems,
) ([]Mount, string) {
	mounts := []Mount{}
	seen := map[string]bool{}
	for _, r := range refs[name] {
		mode := "rw"
		if r.readOnly {
			mode = "ro"
		}
		declared, ok := workspaces[r.target]
		if !ok {
			p.addf("agent %q: workspace %q is not declared under workspaces:", name, r.target)
			continue
		}
		mount := Mount{Name: r.target, Mount: workspaceRoot + "/" + r.target, Mode: mode}
		if declared.Git != nil {
			if r.readOnly {
				p.addf("agent %q: workspace %q is a git workspace and cannot be read-only because git needs write access; "+
					"declare a directory workspace (no git:) and list it as :ro for read access", name, r.target)
				continue
			}
		}
		if seen[mount.Mount] {
			p.addf("agent %q: two workspaces mount at %s", name, mount.Mount)
			continue
		}
		seen[mount.Mount] = true
		mounts = append(mounts, mount)
	}
	return mounts, workdir(name, agent, mounts, p)
}

// workdir applies the rule: an explicit workdir wins, a single workspace is the working
// directory, otherwise it is /workspace.
func workdir(name string, agent Agent, mounts []Mount, p *problems) string {
	if agent.Workdir != "" {
		if strings.HasPrefix(agent.Workdir, "/") {
			return filepath.Clean(agent.Workdir)
		}
		path := filepath.Clean(workspaceRoot + "/" + strings.Trim(agent.Workdir, "/"))
		for _, m := range mounts {
			if m.Mount == path || strings.HasPrefix(path, m.Mount+"/") {
				return path
			}
		}
		known := make([]string, 0, len(mounts))
		for _, m := range mounts {
			known = append(known, strings.TrimPrefix(m.Mount, workspaceRoot+"/"))
		}
		sort.Strings(known)
		p.addf("agent %q: workdir %q is not one of its workspaces (%s)", name, agent.Workdir, strings.Join(known, ", "))
		return workspaceRoot
	}
	if len(mounts) == 1 {
		return mounts[0].Mount
	}
	return workspaceRoot
}
