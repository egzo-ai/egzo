// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/network"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/gitprep"
)

// A git workspace is a directory on the host, in the project: a base clone and one worktree per instance.
//
//	<path>/.base          the base clone, made by `up`, with no files checked out
//	<path>/<instance>     the instance's worktree, on the branch egzo/<instance>, made by `spawn`
//
// Inside a container the names are kept, /workspace/<name>/.base and /workspace/<name>/<instance>, so the
// relative links between a worktree and its base resolve on the host and in the container alike. The git
// work itself runs in short-lived prep containers, on a network of an identity the proxy knows, so the
// clone and the fetch go through the project's proxy under that identity's egress profile and no
// credential is ever the CLI's or the agent's.

const baseName = ".base"

// prepIdentity is the name the base clones of `up` are made under: reserved, so no instance has it.
const prepIdentity = "prep"

// BaseDir is the host directory of a git workspace's base clone.
func BaseDir(ws config.ResolvedWorkspace) string { return filepath.Join(ws.Path, baseName) }

// WorktreeDir is the host directory of the worktree of instance.
func WorktreeDir(ws config.ResolvedWorkspace, instance string) string {
	return filepath.Join(ws.Path, instance)
}

func containerBase(workspace string) string { return workspaceRoot + "/" + workspace + "/" + baseName }

func containerWorktree(workspace, instance string) string {
	return workspaceRoot + "/" + workspace + "/" + instance
}

// worktreeMounts are what a container needs of a git workspace: the instance's own worktree and the base.
func worktreeMounts(workspace string, ws config.ResolvedWorkspace, instance string) []MountSpec {
	return []MountSpec{
		{Bind: true, Source: WorktreeDir(ws, instance), Target: containerWorktree(workspace, instance)},
		{Bind: true, Source: BaseDir(ws), Target: containerBase(workspace)},
	}
}

// instanceWorkdir moves a working directory that is inside a git workspace into the instance's worktree:
// /workspace/<name> is the directory that holds the base and every worktree, and the agent works in its own.
func instanceWorkdir(project *config.Resolved, agent config.ResolvedAgent, instance string) string {
	for _, mount := range agent.Workspaces {
		ws, ok := project.Workspaces[mount.Name]
		if !ok || ws.Git == nil {
			continue
		}
		if agent.Workdir == mount.Mount || strings.HasPrefix(agent.Workdir, mount.Mount+"/") {
			return containerWorktree(mount.Name, instance) + strings.TrimPrefix(agent.Workdir, mount.Mount)
		}
	}
	return agent.Workdir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Checkout is the worktree a git workspace needs for an instance.
type Checkout struct {
	Workspace string
	Instance  string
	Dir       string // the worktree
	Base      string
	URL       string
	Branch    string
}

func (c Checkout) String() string {
	return fmt.Sprintf("workspace %s for %s into %s", c.Workspace, c.Instance, c.Dir)
}

// EnsureDirectories makes the directory workspaces that are missing, empty and as the invoking user (the engine
// would make a missing bind mount as root). Directories that exist are left as they are.
func EnsureDirectories(project *config.Resolved) error {
	for _, name := range sortedKeys(project.Workspaces) {
		if ws := project.Workspaces[name]; ws.Git == nil {
			if err := os.MkdirAll(ws.Path, 0o755); err != nil {
				return fmt.Errorf("create the directory of workspace %q: %w", name, err)
			}
		}
	}
	return nil
}

// ensureMountDirectories is EnsureDirectories for the host directories one agent mounts.
func ensureMountDirectories(agent config.ResolvedAgent) error {
	for _, mount := range agent.Workspaces {
		if mount.HostPath != "" {
			if err := os.MkdirAll(mount.HostPath, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", mount.HostPath, err)
			}
		}
	}
	return nil
}

// leftOver says whether a name has left a worktree or a branch in one of the git workspaces: such a name is not
// free for an automatic name, which would fail on it.
func leftOver(workspaces map[string]config.ResolvedWorkspace, name string) bool {
	for _, ws := range workspaces {
		if ws.Git != nil && (exists(WorktreeDir(ws, name)) || branchExists(BaseDir(ws), gitprep.Branch(name))) {
			return true
		}
	}
	return false
}

// listing returns the agents that list a git workspace, sorted.
func listing(project *config.Resolved, workspace string) []string {
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

// PlanWorktrees lists the worktrees instance needs, one per git workspace it lists.
func PlanWorktrees(project *config.Resolved, instance string) []Checkout {
	var plan []Checkout
	agent, ok := project.Agents[instance]
	if !ok {
		return nil
	}
	for _, mount := range agent.Workspaces {
		ws, ok := project.Workspaces[mount.Name]
		if !ok || ws.Git == nil || mount.HostPath != "" {
			continue
		}
		plan = append(plan, Checkout{
			Workspace: mount.Name, Instance: instance, Dir: WorktreeDir(ws, instance), Base: BaseDir(ws),
			URL: ws.Git.URL, Branch: ws.Git.Branch,
		})
	}
	return plan
}

// CheckWorktrees refuses a plan that would take over what is already there: a worktree directory, or the
// branch of the instance in the base. Both are somebody's work, so only reuse continues with them.
func CheckWorktrees(plan []Checkout, reuse bool) error {
	if reuse {
		return nil
	}
	for _, checkout := range plan {
		if exists(checkout.Dir) {
			return fmt.Errorf("%s already exists: it is the worktree of %q in workspace %q; pass --reuse to continue with it, or remove it",
				checkout.Dir, checkout.Instance, checkout.Workspace)
		}
		if branch := gitprep.Branch(checkout.Instance); branchExists(checkout.Base, branch) {
			return fmt.Errorf("the branch %s already exists in the base clone %s, left by an earlier instance of that name: "+
				"pass --reuse to continue it, or pick another name", branch, checkout.Base)
		}
	}
	return nil
}

// branchExists reads the base's refs on the host: nothing runs git there.
func branchExists(base, branch string) bool {
	git := filepath.Join(base, ".git")
	if exists(filepath.Join(git, "refs", "heads", filepath.FromSlash(branch))) {
		return true
	}
	packed, err := os.ReadFile(filepath.Join(git, "packed-refs"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[1] == "refs/heads/"+branch {
			return true
		}
	}
	return false
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

// WorktreeOptions are what `spawn` may say about the worktrees it makes.
type WorktreeOptions struct {
	NoFetch bool
	Reuse   bool
}

// warningPrefix starts a line of a git job's output that is a warning for the person, not an answer.
const warningPrefix = "egzo-warning: "

// RunWorktreePreps makes the worktrees of an instance, one at a time, on the instance's own network, so the
// base is cloned (when it is missing) and fetched through the proxy under the instance's identity.
func RunWorktreePreps(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user string, tokens map[string]string, plan []Checkout, options WorktreeOptions, out io.Writer) error {
	if len(plan) == 0 {
		return nil
	}
	if err := ensureImage(ctx, c, image); err != nil {
		return err
	}
	for _, checkout := range plan {
		fmt.Fprintf(out, "worktree %s\n", checkout)
		madeBase, madeDir := !exists(checkout.Base), !exists(checkout.Dir)
		for _, d := range []string{checkout.Base, checkout.Dir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", d, err)
			}
		}
		cmd := []string{"/egzo", "prep", "git", "worktree", "--url", checkout.URL,
			"--base", containerBase(checkout.Workspace), "--agent", checkout.Instance}
		if checkout.Branch != "" {
			cmd = append(cmd, "--branch", checkout.Branch)
		}
		if options.NoFetch {
			cmd = append(cmd, "--no-fetch")
		}
		if options.Reuse {
			cmd = append(cmd, "--reuse")
		}
		cmd = append(cmd, containerWorktree(checkout.Workspace, checkout.Instance))
		ws := project.Workspaces[checkout.Workspace]
		stdout, err := runGit(ctx, c, project, dir, image, user, checkout.Instance, tokens[checkout.Instance],
			InstanceNetwork(project.Name, checkout.Instance), cmd, worktreeMounts(checkout.Workspace, ws, checkout.Instance))
		if err != nil {
			// leave nothing half-made behind: an empty directory made for this job is not a worktree
			if madeDir {
				os.Remove(checkout.Dir)
			}
			if madeBase {
				os.Remove(checkout.Base)
			}
			return gitFailure(project, checkout.Instance, checkout.Workspace, checkout.URL, err)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if warning, ok := strings.CutPrefix(line, warningPrefix); ok {
				fmt.Fprintf(out, "warning: workspace %s: %s\n", checkout.Workspace, warning)
			}
		}
	}
	return nil
}

// runGit runs one git job in a prep container on a network the proxy serves, as agent.
func runGit(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user, agent, token, network string, cmd []string, mounts []MountSpec) (string, error) {
	env := proxyEnv(project.Name, agent, token)
	env["HOME"] = "/tmp"
	all := append([]MountSpec{{Source: project.Name + "_ca", Target: caAgentDir, ReadOnly: true}}, mounts...)
	return RunPrep(ctx, c, PrepSpec{
		Image:    image,
		Cmd:      cmd,
		User:     user,
		Env:      envList(env),
		Mounts:   all,
		Network:  network,
		Identity: engine.Identity{Project: project.Name, Service: "prep", Kind: "prep", ProjectDir: dir},
	})
}

func gitFailure(project *config.Resolved, agent, workspace, rawURL string, err error) error {
	host := rawURL
	if parsed, perr := url.Parse(rawURL); perr == nil {
		host = parsed.Hostname()
	}
	profile := project.Agents[agent].Egress
	return fmt.Errorf("agent %q: could not prepare git workspace %q from %s: %w\n"+
		"the agent's egress profile %q must allow %s", agent, workspace, rawURL, err, profile, host)
}

// CloneBases makes the base clone of every git workspace an agent lists and that has none yet, as `up`'s last
// step. A failure is not fatal: it is written to warn, and the next `spawn` (or `egzo workspace fetch`) tries again.
// The clone goes through the proxy under the egress profile of the first template that lists the workspace.
func CloneBases(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user string, out, warn io.Writer) {
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		agents := listing(project, name)
		if ws.Git == nil || len(agents) == 0 {
			continue
		}
		if exists(filepath.Join(BaseDir(ws), ".git")) {
			if have := baseURL(BaseDir(ws)); have != "" && have != ws.Git.URL {
				fmt.Fprintf(warn, "warning: workspace %q: the base clone %s has the url %s, but egzo.yaml says %s: it is left as it is "+
					"(remove the directory to clone again)\n", name, BaseDir(ws), have, ws.Git.URL)
			}
			continue
		}
		fmt.Fprintf(out, "clone workspace %s into %s\n", name, BaseDir(ws))
		if err := cloneBase(ctx, c, project, dir, image, user, name, ws, agents[0]); err != nil {
			fmt.Fprintf(warn, "warning: workspace %q was not cloned: %v\n", name, err)
		}
	}
}

func cloneBase(ctx context.Context, c *engine.Client, project *config.Resolved, dir, image, user, name string, ws config.ResolvedWorkspace, agent string) error {
	profile := project.Agents[agent].Egress
	if err := ensureImage(ctx, c, image); err != nil {
		return err
	}
	madeBase := !exists(BaseDir(ws))
	if err := os.MkdirAll(BaseDir(ws), 0o755); err != nil {
		return err
	}
	cmd := []string{"/egzo", "prep", "git", "clone", "--url", ws.Git.URL}
	if ws.Git.Branch != "" {
		cmd = append(cmd, "--branch", ws.Git.Branch)
	}
	cmd = append(cmd, containerBase(name))
	err := withPrepIdentity(ctx, c, project, dir, profile, func(network, token string) error {
		_, err := runGit(ctx, c, project, dir, image, user, prepIdentity, token, network, cmd,
			[]MountSpec{{Bind: true, Source: BaseDir(ws), Target: containerBase(name)}})
		return err
	})
	if err != nil {
		if madeBase {
			os.Remove(BaseDir(ws))
		}
		return gitFailure(project, agent, name, ws.Git.URL, err)
	}
	return nil
}

// withPrepIdentity gives fn a network the proxy serves and a token that is bound to profile, then takes both
// away again. It is how a job that belongs to no instance reaches the world: through the proxy, like an agent.
func withPrepIdentity(ctx context.Context, c *engine.Client, project *config.Resolved, dir, profile string, fn func(network, token string) error) (err error) {
	net := InstanceNetwork(project.Name, prepIdentity)
	control, proxy := project.Name+"-control-1", project.Name+"-proxy-1"
	// a network left by a run that was killed
	_ = c.API.NetworkRemove(ctx, net)
	identity := engine.Identity{Project: project.Name, Service: "prep", Kind: "prep", ProjectDir: dir}
	if _, err := c.API.NetworkCreate(ctx, net, network.CreateOptions{Driver: "bridge", Internal: true, Labels: identity.Labels()}); err != nil {
		return fmt.Errorf("create network %s: %w", net, err)
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() {
		unbindProxy(cleanup, c, project.Name, prepIdentity)
		unregisterControl(cleanup, c, project.Name, prepIdentity)
		c.API.NetworkDisconnect(cleanup, net, control, true)
		c.API.NetworkDisconnect(cleanup, net, proxy, true)
		c.API.NetworkRemove(cleanup, net)
	}()
	for _, peer := range []struct{ container, alias string }{{control, "control"}, {proxy, "proxy"}} {
		if err := c.API.NetworkConnect(ctx, net, peer.container, &network.EndpointSettings{Aliases: []string{peer.alias}}); err != nil {
			return fmt.Errorf("connect %s to %s: %w", peer.container, net, err)
		}
	}
	unregisterControl(ctx, c, project.Name, prepIdentity)
	if err := registerControl(ctx, c, project.Name, prepIdentity); err != nil {
		return fmt.Errorf("register %s with the control sidecar: %w", prepIdentity, err)
	}
	tokens, err := fetchTokens(ctx, c, control, []string{prepIdentity})
	if err != nil {
		return err
	}
	if err := bindProxy(ctx, c, project.Name, prepIdentity, tokens[prepIdentity], profile); err != nil {
		return err
	}
	return fn(net, tokens[prepIdentity])
}

// baseURL reads the url of the origin of a base clone from its config, or "" when it has none. Nothing runs
// git on the host.
func baseURL(base string) string {
	data, err := os.ReadFile(filepath.Join(base, ".git", "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		text := strings.TrimSpace(string(line))
		switch {
		case strings.HasPrefix(text, "["):
			inOrigin = text == `[remote "origin"]`
		case inOrigin && strings.HasPrefix(text, "url"):
			if _, value, ok := strings.Cut(text, "="); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// RemoveWorktrees deletes the worktrees of the given instances, in every git workspace, and what the base
// keeps about them. A worktree is the instance's own: it goes with the instance, unchecked. The branch stays
// in the base, so what was committed outlives the instance.
func RemoveWorktrees(project *config.Resolved, instances []string, out io.Writer) error {
	var errs []error
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		if ws.Git == nil {
			continue
		}
		for _, instance := range instances {
			dir := WorktreeDir(ws, instance)
			info, err := os.Lstat(dir)
			if err != nil {
				forgetWorktree(BaseDir(ws), dir) // removed by hand: what the base keeps about it would block the name
				continue
			}
			if !info.IsDir() || !isWorktree(dir) {
				continue // not a worktree: never delete anything else
			}
			fmt.Fprintf(out, "remove worktree %s\n", dir)
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
				continue
			}
			forgetWorktree(BaseDir(ws), dir)
		}
	}
	return errors.Join(errs...)
}

// isWorktree says whether dir is a linked worktree: its .git is a file that points at a gitdir, where a repository
// of its own has a .git directory.
func isWorktree(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	return err == nil && strings.HasPrefix(string(data), "gitdir:")
}

// forgetWorktree removes what the base keeps about the worktree at dir: the directory git prunes once the
// worktree is gone. Only that worktree's: another instance's is not mounted where this runs, and a prune
// would take it for gone.
func forgetWorktree(base, dir string) {
	entries, err := os.ReadDir(filepath.Join(base, ".git", "worktrees"))
	if err != nil {
		return
	}
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
		if filepath.Clean(target) == filepath.Join(dir, ".git") {
			os.RemoveAll(meta)
		}
	}
}
