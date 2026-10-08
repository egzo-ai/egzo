// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Git workspaces live on the host so work survives agents and projects. The CLI decides the layout;
// the git work itself runs in short-lived prep containers on the agent's own network, so the clone
// goes through the project's proxy under the agent's egress profile and no credential is ever the
// CLI's or the agent's.
//
//	clone     <path>/<instance>   one independent clone per instance
//	shared    <path>/shared       one checkout used by every instance that lists it
//	worktree  <path>/<instance>   a worktree of the base clone <path>/.base
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
			if mount.Name == workspace && mount.HostPath == "" {
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
		names := []string{filepath.Base(checkout.Dir)}
		if checkout.Base != "" {
			names = append(names, filepath.Base(checkout.Base))
		}
		if err := markCheckouts(filepath.Dir(checkout.Dir), names...); err != nil {
			return fmt.Errorf("record the checkout %s: %w", checkout.Dir, err)
		}
	}
	return nil
}

// checkoutRegistry is the file in a git workspace's path that names the checkouts egzo made there. A
// workspace path may hold other things (a path of `./repos` next to the user's own clones), and
// `down --workspaces` must only offer to remove what egzo made.
const checkoutRegistry = ".egzo-checkouts"

// markCheckouts records directories under path as made by egzo.
func markCheckouts(path string, names ...string) error {
	known := managedCheckouts(path)
	var added []string
	for _, name := range names {
		if !known[name] {
			known[name] = true
			added = append(added, name)
		}
	}
	if len(added) == 0 {
		return nil
	}
	file, err := os.OpenFile(filepath.Join(path, checkoutRegistry), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(strings.Join(added, "\n") + "\n")
	return err
}

// recordCheckouts records, for each git workspace an instance lists, the directories it uses: its own
// clone or worktree, the shared checkout, and a worktree workspace's base.
func recordCheckouts(view *config.Resolved, instance string) error {
	agent, ok := view.Agents[instance]
	if !ok {
		return nil
	}
	for _, mount := range agent.Workspaces {
		ws, ok := view.Workspaces[mount.Name]
		if mount.HostPath != "" || !ok || ws.Git == nil {
			continue
		}
		if err := os.MkdirAll(ws.Path, 0o755); err != nil {
			return err
		}
		names := []string{filepath.Base(CheckoutDir(ws, instance))}
		if ws.Mode == "worktree" {
			names = append(names, filepath.Base(BaseDir(ws)))
		}
		if err := markCheckouts(ws.Path, names...); err != nil {
			return fmt.Errorf("record the checkouts of %s: %w", instance, err)
		}
	}
	return nil
}

// managedCheckouts reads the names markCheckouts wrote.
func managedCheckouts(path string) map[string]bool {
	known := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(path, checkoutRegistry))
	if err != nil {
		return known
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.ContainsAny(line, "/\\") && line != ".." && line != "." {
			known[line] = true
		}
	}
	return known
}

// GitDirs lists the host directories that hold git checkouts egzo made for the project's workspaces,
// whether or not an instance still exists for them: what `down --workspaces` may remove. They are found
// on disk, so work left by an instance that was removed is found too. A symbolic link egzo recorded is
// listed only so that removing it can be refused.
func GitDirs(project *config.Resolved) []string {
	var dirs []string
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		if ws.Git == nil {
			continue
		}
		managed := managedCheckouts(ws.Path)
		entries, err := os.ReadDir(ws.Path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(ws.Path, entry.Name())
			if !managed[entry.Name()] {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 || entry.Name() == ".base" || (entry.IsDir() && exists(filepath.Join(path, ".git"))) {
				dirs = append(dirs, path)
			}
		}
	}
	return dirs
}

// InstanceGitDirs lists the checkouts that belong to one instance alone: the clone or worktree of each
// git workspace it lists. A shared checkout and a worktree base belong to the project.
func InstanceGitDirs(project *config.Resolved, instance string) []string {
	var dirs []string
	agent, ok := project.Agents[instance]
	if !ok {
		return nil
	}
	for _, mount := range agent.Workspaces {
		if mount.HostPath != "" {
			continue
		}
		ws, ok := project.Workspaces[mount.Name]
		if !ok || ws.Git == nil || ws.Mode == "shared" {
			continue
		}
		dirs = append(dirs, CheckoutDir(ws, instance))
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

// Unsaved describes what a checkout holds that exists nowhere else.
type Unsaved struct {
	Dir         string
	Uncommitted int
	Unpushed    int // commits on a branch no remote has, or on no branch at all
	Stashes     int
	Ignored     int // files and directories git ignores: not in any commit, so lost with the checkout
}

// Blocks reports whether removing the checkout would lose work. Ignored files only inform: a build
// directory is ignored too, and refusing for it would make every removal need --force.
func (u Unsaved) Blocks() bool { return u.Uncommitted > 0 || u.Unpushed > 0 || u.Stashes > 0 }

var inspection = regexp.MustCompile(`^(\S+) uncommitted=(\d+) unpushed=(\d+) stashes=(\d+) ignored=(\d+)$`)

// parseInspection reads the answer of `egzo prep git status` about one directory. Anything else, no line,
// a line about another directory, or more than one, is an error: an answer that cannot be read must stop
// the removal, never pass for "clean".
func parseInspection(target, stdout string) (Unsaved, error) {
	var found []Unsaved
	for _, line := range strings.Split(stdout, "\n") {
		m := inspection.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if m[1] != target {
			return Unsaved{}, fmt.Errorf("the inspection of %s answered about %s", target, m[1])
		}
		var u Unsaved
		fmt.Sscan(m[2], &u.Uncommitted)
		fmt.Sscan(m[3], &u.Unpushed)
		fmt.Sscan(m[4], &u.Stashes)
		fmt.Sscan(m[5], &u.Ignored)
		found = append(found, u)
	}
	if len(found) != 1 {
		return Unsaved{}, fmt.Errorf("the inspection of %s gave %d answers, want exactly one", target, len(found))
	}
	return found[0], nil
}

// InspectCheckouts asks what each existing checkout holds, each in a prep container of its own (no
// network, no credentials, only that checkout mounted read-only): what a checkout's own git
// configuration can run then sees nothing of any other agent's work.
func InspectCheckouts(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user string) ([]Unsaved, error) {
	return InspectDirs(ctx, c, project.Name, GitDirs(project), dir, image, user)
}

// InspectDirs is InspectCheckouts for the given directories; those that are not checkouts are left out.
func InspectDirs(ctx context.Context, c *engine.Client, projectName string, dirs []string, dir, image, user string) ([]Unsaved, error) {
	var existing []string
	for _, d := range dirs {
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
	results := make([]Unsaved, len(existing))
	errs := make([]error, len(existing))
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, d := range existing {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			const target = "/check"
			mounts := []MountSpec{{Bind: true, Source: d, Target: target, ReadOnly: true}}
			if base, ok := worktreeBaseMount(d); ok {
				mounts = append(mounts, base)
			}
			stdout, err := RunPrep(ctx, c, PrepSpec{
				Image: image, Cmd: []string{"/egzo", "prep", "git", "status", target}, User: user,
				Mounts:   mounts,
				Env:      []string{"HOME=/tmp", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*"},
				Identity: engine.Identity{Project: projectName, Service: "prep", Kind: "prep", ProjectDir: dir},
			})
			if err == nil {
				results[i], err = parseInspection(target, stdout)
			}
			results[i].Dir = d
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", existing[i], err)
		}
	}
	var found []Unsaved
	for _, u := range results {
		if u.Blocks() || u.Ignored > 0 {
			found = append(found, u)
		}
	}
	return found, nil
}

// worktreeBaseMount is the mount a worktree needs to be read: its .git is a file that points into the base
// clone at the path the base has inside every container (/.egzo/base/<workspace>), so an inspection that
// mounts only the worktree finds no repository. The base is read-only: status takes no lock it must have.
func worktreeBaseMount(dir string) (MountSpec, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return MountSpec{}, false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return MountSpec{}, false
	}
	rest, ok := strings.CutPrefix(target, baseMount+"/")
	if !ok || rest == "" {
		return MountSpec{}, false
	}
	workspace, _, _ := strings.Cut(rest, "/")
	if workspace == "" || workspace == "." || workspace == ".." {
		return MountSpec{}, false
	}
	return MountSpec{Bind: true, Source: filepath.Join(filepath.Dir(dir), ".base"), Target: baseMountPath(workspace), ReadOnly: true}, true
}

func (u Unsaved) String() string {
	var parts []string
	if u.Uncommitted > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted file(s)", u.Uncommitted))
	}
	if u.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed commit(s)", u.Unpushed))
	}
	if u.Stashes > 0 {
		parts = append(parts, fmt.Sprintf("%d stash(es)", u.Stashes))
	}
	if u.Ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignored file(s) or director(ies) that are in no commit", u.Ignored))
	}
	return fmt.Sprintf("%s: %s", u.Dir, strings.Join(parts, ", "))
}

// RemovalProblem says why dir must not be removed as a checkout, or nil when it may be: it has to be a
// real git checkout (not a symlink, not any directory that happens to bear an agent's name), and not the
// project directory, one of its parents, the home directory or the root.
func RemovalProblem(dir, projectDir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", dir)
	}
	if !exists(filepath.Join(dir, ".git")) {
		return fmt.Errorf("%s is not a git checkout (no .git in it)", dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	project, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		project = projectDir
	}
	home, _ := os.UserHomeDir()
	switch {
	case real == "/" || real == filepath.Clean(home) || real == project:
		return fmt.Errorf("%s is the project directory, the home directory or the root", dir)
	case strings.HasPrefix(project+"/", real+"/"):
		return fmt.Errorf("%s contains the project directory", dir)
	}
	return nil
}

// StopAgents stops the running agent containers and returns them, so a refused removal can start them
// again. Nothing may write to a checkout while it is inspected and while the person decides.
func StopAgents(ctx context.Context, c *engine.Client, observed Observed) ([]Resource, error) {
	var stopped []Resource
	timeout := stopTimeoutSeconds
	for _, r := range observed.Resources {
		if r.Type == "container" && r.Kind == kindAgent && r.State == "running" {
			if err := c.API.ContainerStop(ctx, r.ID, container.StopOptions{Timeout: &timeout}); err != nil {
				StartAgents(ctx, c, stopped)
				return nil, fmt.Errorf("stop %s: %w", r.Instance, err)
			}
			stopped = append(stopped, r)
		}
	}
	return stopped, nil
}

// StartAgents starts containers StopAgents stopped.
func StartAgents(ctx context.Context, c *engine.Client, stopped []Resource) {
	for _, r := range stopped {
		_ = c.API.ContainerStart(context.WithoutCancel(ctx), r.ID, container.StartOptions{})
	}
}
