package stack

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Git workspaces live on the host so work survives agents and projects. The CLI decides the layout;
// the git work itself runs in short-lived prep containers on the agent's own network, so the clone
// goes through the project's proxy under the agent's egress profile and no credential is ever the
// CLI's or the agent's.
//
//	clone     <path>/<agent>      one independent clone per agent
//	shared    <path>/shared       one checkout used by every agent that lists it
//	worktree  <path>/<agent>      a worktree of the base clone <path>/.base
//
// Inside every container the checkout is /workspace/<name> and, for a worktree, the base is
// /.egzo/base/<name>: prep and agent see the same paths, so the absolute links git writes between a
// worktree and its base are valid in both. (From the host those links do not resolve.)

const baseMount = "/.egzo/base"

func baseMountPath(workspace string) string { return baseMount + "/" + workspace }

// CheckoutDir is the host directory of workspace ws for agent.
func CheckoutDir(ws config.ResolvedWorkspace, agent string) string {
	if ws.Mode == "shared" {
		return filepath.Join(ws.Path, "shared")
	}
	return filepath.Join(ws.Path, agent)
}

// BaseDir is the host directory of a worktree workspace's base clone.
func BaseDir(ws config.ResolvedWorkspace) string { return filepath.Join(ws.Path, ".base") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Checkout is one directory a git workspace needs for an agent.
type Checkout struct {
	Workspace string
	Agent     string // the agent whose network and identity the clone uses
	Mode      string
	Dir       string
	Base      string // worktree only
	URL       string
	Branch    string
}

func (c Checkout) String() string {
	return fmt.Sprintf("workspace %s for %s into %s", c.Workspace, c.Agent, c.Dir)
}

// existingMode says how the checkouts already under a git workspace's path were made, or "" when
// there are none.
func existingMode(ws config.ResolvedWorkspace) string {
	switch {
	case exists(filepath.Join(ws.Path, "shared", ".git")):
		return "shared"
	case exists(filepath.Join(BaseDir(ws), ".git")):
		return "worktree"
	}
	entries, _ := os.ReadDir(ws.Path)
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "shared" && !strings.HasPrefix(entry.Name(), ".") && exists(filepath.Join(ws.Path, entry.Name(), ".git")) {
			return "clone"
		}
	}
	return ""
}

// agentsListing returns the agents that list a git workspace directly, sorted.
func agentsListing(project *config.Resolved, workspace string) []string {
	var agents []string
	for name, agent := range project.Agents {
		for _, mount := range agent.Workspaces {
			if mount.Name == workspace && mount.From == "" && mount.HostPath == "" {
				agents = append(agents, name)
				break
			}
		}
	}
	sort.Strings(agents)
	return agents
}

// PlanGit lists the checkouts that do not exist yet. It refuses a workspace whose existing
// checkouts were made in another mode: they are never converted, because they hold work.
func PlanGit(project *config.Resolved) ([]Checkout, error) {
	var plan []Checkout
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		if ws.Git == nil {
			continue
		}
		agents := agentsListing(project, name)
		if len(agents) == 0 {
			continue
		}
		if have := existingMode(ws); have != "" && have != ws.Mode {
			return nil, fmt.Errorf("workspace %q is configured with mode %s, but %s holds %s checkouts: "+
				"egzo never converts an existing checkout because it may hold unpushed work; "+
				"restore the mode, or remove the directory (egzo down --workspaces checks for unsaved work first)", name, ws.Mode, ws.Path, have)
		}
		for _, agent := range agents {
			dir := CheckoutDir(ws, agent)
			if exists(filepath.Join(dir, ".git")) {
				continue
			}
			if ws.Mode == "shared" && len(plan) > 0 && plan[len(plan)-1].Workspace == name {
				continue // one checkout for everyone, made through the first agent
			}
			checkout := Checkout{Workspace: name, Agent: agent, Mode: ws.Mode, Dir: dir, URL: ws.Git.URL, Branch: ws.Git.Branch}
			if ws.Mode == "worktree" {
				checkout.Base = BaseDir(ws)
			}
			plan = append(plan, checkout)
		}
	}
	return plan, nil
}

// proxyEnv is how an agent, or a prep container acting for it, reaches the world: only through the
// project's proxy, as that agent, trusting the project CA.
func proxyEnv(project, agent, token string) map[string]string {
	proxyURL := fmt.Sprintf("http://%s:%s@proxy:%s", agent, token, proxyPort)
	bundle, certificate := caAgentDir+"/ca-bundle.crt", caAgentDir+"/ca.crt"
	return map[string]string{
		"HTTPS_PROXY": proxyURL, "https_proxy": proxyURL, "HTTP_PROXY": proxyURL, "http_proxy": proxyURL,
		"NO_PROXY": "control,localhost,127.0.0.1", "no_proxy": "control,localhost,127.0.0.1",
		"SSL_CERT_FILE": bundle, "REQUESTS_CA_BUNDLE": bundle, "CURL_CA_BUNDLE": bundle, "GIT_SSL_CAINFO": bundle,
		"NODE_EXTRA_CA_CERTS": certificate,
	}
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, key := range sortedKeys(env) {
		out = append(out, key+"="+env[key])
	}
	return out
}

// RunGitPreps makes the checkouts, one at a time: worktrees share a base clone, and a few clones
// are not worth the complexity of locking.
func RunGitPreps(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user string, tokens map[string]string, plan []Checkout, out io.Writer) error {
	if len(plan) == 0 {
		return nil
	}
	if err := ensureImage(ctx, c, image); err != nil {
		return err
	}
	for _, checkout := range plan {
		fmt.Fprintf(out, "clone %s\n", checkout)
		dirs := []string{checkout.Dir}
		if checkout.Base != "" {
			dirs = append(dirs, checkout.Base)
		}
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", d, err)
			}
		}
		target := workspaceRoot + "/" + checkout.Workspace
		cmd := []string{"/egzo", "prep", "git"}
		switch checkout.Mode {
		case "worktree":
			cmd = append(cmd, "worktree", "--url", checkout.URL, "--base", baseMountPath(checkout.Workspace), "--agent", checkout.Agent)
		default:
			cmd = append(cmd, "clone", "--url", checkout.URL)
		}
		if checkout.Branch != "" {
			cmd = append(cmd, "--branch", checkout.Branch)
		}
		cmd = append(cmd, target)

		env := proxyEnv(project.Name, checkout.Agent, tokens[checkout.Agent])
		env["HOME"] = "/tmp"
		mounts := []MountSpec{
			{Source: project.Name + "_ca", Target: caAgentDir, ReadOnly: true},
			{Bind: true, Source: checkout.Dir, Target: target},
		}
		if checkout.Base != "" {
			mounts = append(mounts, MountSpec{Bind: true, Source: checkout.Base, Target: baseMountPath(checkout.Workspace)})
		}
		_, err := RunPrep(ctx, c, PrepSpec{
			Image:    image,
			Cmd:      cmd,
			User:     user,
			Env:      envList(env),
			Mounts:   mounts,
			Network:  project.Name + "_" + checkout.Agent,
			Identity: engine.Identity{Project: project.Name, Service: "prep", Kind: "prep", ProjectDir: dir},
		})
		if err != nil {
			// leave nothing half-made behind: an empty directory is not a checkout, but a failed
			// clone must not look like one on the next run
			os.Remove(checkout.Dir)
			host := checkout.URL
			if parsed, perr := url.Parse(checkout.URL); perr == nil {
				host = parsed.Hostname()
			}
			profile := project.Agents[checkout.Agent].Egress
			return fmt.Errorf("agent %q: could not prepare git workspace %q from %s: %w\n"+
				"the agent's egress profile %q must allow %s", checkout.Agent, checkout.Workspace, checkout.URL, err, profile, host)
		}
	}
	return nil
}

// Dirs lists the host directories of every git checkout a project's workspaces can have, whether
// or not they exist: what `down --workspaces` may remove.
func GitDirs(project *config.Resolved) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		if ws.Git == nil {
			continue
		}
		for _, agent := range agentsListing(project, name) {
			add(CheckoutDir(ws, agent))
		}
		if ws.Mode == "worktree" {
			add(BaseDir(ws))
		}
	}
	return dirs
}

// WorkspaceRoots are the parent directories of GitDirs, which go when they are left empty.
func WorkspaceRoots(project *config.Resolved) []string {
	var roots []string
	for _, name := range sortedKeys(project.Workspaces) {
		if ws := project.Workspaces[name]; ws.Git != nil {
			roots = append(roots, ws.Path)
		}
	}
	return roots
}

// Unsaved describes the work a checkout holds that exists nowhere else.
type Unsaved struct {
	Dir         string
	Uncommitted int
	Unpushed    int
}

// InspectCheckouts asks a prep container (no network, no credentials) which of the existing
// checkouts hold work that exists nowhere else.
func InspectCheckouts(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user string) ([]Unsaved, error) {
	var existing []string
	for _, d := range GitDirs(project) {
		if exists(filepath.Join(d, ".git")) {
			existing = append(existing, d)
		}
	}
	if len(existing) == 0 {
		return nil, nil
	}
	if err := ensureImage(ctx, c, image); err != nil {
		return nil, err
	}
	cmd := []string{"/egzo", "prep", "git", "status"}
	var mounts []MountSpec
	for i, d := range existing {
		target := fmt.Sprintf("/check/%d", i)
		cmd = append(cmd, target)
		mounts = append(mounts, MountSpec{Bind: true, Source: d, Target: target, ReadOnly: true})
	}
	out, err := RunPrep(ctx, c, PrepSpec{
		Image: image, Cmd: cmd, User: user, Mounts: mounts,
		Env:      []string{"HOME=/tmp", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*"},
		Identity: engine.Identity{Project: project.Name, Service: "prep", Kind: "prep", ProjectDir: dir},
	})
	if err != nil {
		return nil, fmt.Errorf("inspect the git workspaces: %w", err)
	}
	var unsaved []Unsaved
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		var target string
		var uncommitted, unpushed int
		if n, _ := fmt.Sscanf(scanner.Text(), "%s uncommitted=%d unpushed=%d", &target, &uncommitted, &unpushed); n == 3 && (uncommitted > 0 || unpushed > 0) {
			var index int
			fmt.Sscanf(target, "/check/%d", &index)
			unsaved = append(unsaved, Unsaved{Dir: existing[index], Uncommitted: uncommitted, Unpushed: unpushed})
		}
	}
	return unsaved, nil
}

func (u Unsaved) String() string {
	var parts []string
	if u.Uncommitted > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted file(s)", u.Uncommitted))
	}
	if u.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed commit(s)", u.Unpushed))
	}
	return fmt.Sprintf("%s: %s", u.Dir, strings.Join(parts, ", "))
}
