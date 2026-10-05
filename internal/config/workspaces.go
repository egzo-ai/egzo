package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const workspaceRoot = "/workspace"

// ResolvedWorkspace is a declared workspace with defaults applied.
type ResolvedWorkspace struct {
	Git  *Git   `yaml:"git,omitempty"`
	Mode string `yaml:"mode,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// Mount is one workspace as an agent sees it.
type Mount struct {
	Name     string `yaml:"name"`
	Mount    string `yaml:"mount"`
	Mode     string `yaml:"mode"`
	HostPath string `yaml:"host_path,omitempty"`
	From     string `yaml:"from,omitempty"`
}

var gitModes = map[string]bool{"clone": true, "worktree": true, "shared": true}

func resolveWorkspaces(file *File, dir string, p *problems) map[string]ResolvedWorkspace {
	resolved := map[string]ResolvedWorkspace{}
	for name, workspace := range file.Workspaces {
		if !identifier.MatchString(name) {
			continue // checkNames reports it
		}
		if workspace.Git == nil {
			if workspace.Mode != "" {
				p.addf("workspace %q: mode only applies to git workspaces", name)
			}
			if workspace.Path != "" {
				p.addf("workspace %q: path only applies to git workspaces", name)
			}
			resolved[name] = ResolvedWorkspace{}
			continue
		}

		checkGit(name, workspace.Git, p)
		mode := workspace.Mode
		if mode == "" {
			mode = "clone"
		}
		if !gitModes[mode] {
			p.addf("workspace %q: unknown mode %q (use clone, worktree or shared)", name, mode)
		}
		path := workspace.Path
		switch {
		case path == "":
			path = filepath.Join(dir, ".egzo", "workspaces", name)
		case !filepath.IsAbs(path):
			path = filepath.Join(dir, path)
		}
		resolved[name] = ResolvedWorkspace{Git: workspace.Git, Mode: mode, Path: filepath.Clean(path)}
	}
	return resolved
}

type refKind int

const (
	refWorkspace refKind = iota
	refHost
	refAgent
)

// ref is a parsed entry of an agent's workspaces list: name[:ro], ./path[:ro] or agent/name:ro.
type ref struct {
	raw      string
	kind     refKind
	target   string
	agent    string
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
	if rest == "" || strings.Contains(rest, ":") {
		return parsed, false
	}
	parsed.target = rest
	switch {
	case strings.HasPrefix(rest, "./"), strings.HasPrefix(rest, "../"), strings.HasPrefix(rest, "/"):
		parsed.kind = refHost
	case strings.Contains(rest, "/"):
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return parsed, false
		}
		parsed.kind = refAgent
		parsed.agent, parsed.target = parts[0], parts[1]
	default:
		parsed.kind = refWorkspace
	}
	return parsed, true
}

// agentRefs parses every agent's workspaces list once so references between agents can be checked.
func agentRefs(file *File, p *problems) map[string][]ref {
	refs := map[string][]ref{}
	for name, agent := range file.Agents {
		for _, raw := range agent.Workspaces {
			parsed, ok := parseRef(raw)
			if !ok {
				p.addf("agent %q: unsupported workspace reference %q (use name, name:ro, ./path, /abs/path or agent/name:ro)", name, raw)
				continue
			}
			refs[name] = append(refs[name], parsed)
		}
	}
	return refs
}

func lists(refs []ref, workspace string) bool {
	for _, r := range refs {
		if r.kind == refWorkspace && r.target == workspace {
			return true
		}
	}
	return false
}

// resolveMounts turns an agent's references into mounts and its working directory.
func resolveMounts(
	file *File, name string, agent Agent, dir string,
	workspaces map[string]ResolvedWorkspace, refs map[string][]ref, p *problems,
) ([]Mount, string) {
	mounts := []Mount{}
	seen := map[string]bool{}
	add := func(m Mount) {
		if seen[m.Mount] {
			p.addf("agent %q: two workspaces mount at %s", name, m.Mount)
			return
		}
		seen[m.Mount] = true
		mounts = append(mounts, m)
	}

	for _, r := range refs[name] {
		mode := "rw"
		if r.readOnly {
			mode = "ro"
		}
		switch r.kind {
		case refWorkspace:
			declared, ok := workspaces[r.target]
			if !ok {
				p.addf("agent %q: workspace %q is not declared under workspaces:", name, r.target)
				continue
			}
			if declared.Git != nil && r.readOnly {
				p.addf("agent %q: workspace %q is a git workspace and cannot be read-only because git needs write access; "+
					"to read another agent's copy, reference it as <agent>/%s:ro", name, r.target, r.target)
				continue
			}
			add(Mount{Name: r.target, Mount: workspaceRoot + "/" + r.target, Mode: mode})

		case refHost:
			path := r.target
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			path = filepath.Clean(path)
			if path == "/" {
				p.addf("agent %q: workspace %q would mount the whole filesystem root", name, r.raw)
				continue
			}
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				p.addf("agent %q: workspace %q is not an existing directory (%s)", name, r.raw, path)
				continue
			}
			base := filepath.Base(path)
			add(Mount{Name: base, Mount: workspaceRoot + "/" + base, Mode: mode, HostPath: path})

		case refAgent:
			switch {
			case r.agent == name:
				p.addf("agent %q: workspace reference %q points at itself", name, r.raw)
			case !hasAgent(file, r.agent):
				p.addf("agent %q: workspace reference %q names unknown agent %q", name, r.raw, r.agent)
			case !r.readOnly:
				p.addf("agent %q: workspace reference %q must be read-only (%s:ro)", name, r.raw, strings.TrimSuffix(r.raw, ":rw"))
			case !lists(refs[r.agent], r.target):
				p.addf("agent %q: workspace reference %q: agent %q does not list workspace %q", name, r.raw, r.agent, r.target)
			default:
				add(Mount{Name: r.target, Mount: workspaceRoot + "/" + r.agent + "/" + r.target, Mode: "ro", From: r.agent})
			}
		}
	}

	return mounts, workdir(name, agent, mounts, p)
}

func hasAgent(file *File, name string) bool {
	_, ok := file.Agents[name]
	return ok
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
