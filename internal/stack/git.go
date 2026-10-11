// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/network"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/gitprep"
)

// A git workspace is a base clone and one worktree per instance, each in an engine volume (see volumes.go):
//
//	/workspace/<name>/.base         the base clone, made by `up`, with no files checked out
//	/workspace/<name>/<instance>    the instance's worktree, on the branch egzo/<instance>, made by `spawn`
//
// In every container that has them the two volumes are mounted side by side under these names, so the relative
// links git writes between a worktree and its base resolve. The git work runs in short-lived prep containers, on a
// network of an identity the proxy knows, so the clone and the fetch go through the project's proxy under that
// identity's egress profile and no credential is ever the CLI's or the agent's. Nothing here touches the host.

const baseName = ".base"

// prepIdentity is the name the base clones of `up` are made under: reserved, so no instance has it.
const prepIdentity = "prep"

func containerBase(workspace string) string { return workspaceRoot + "/" + workspace + "/" + baseName }

func containerWorktree(workspace, instance string) string {
	return workspaceRoot + "/" + workspace + "/" + instance
}

// worktreeMounts are what a container needs of a git workspace: the instance's own worktree and the base.
func worktreeMounts(project, workspace, instance string) []MountSpec {
	return []MountSpec{
		{Source: WorktreeVolume(project, workspace, instance), Target: containerWorktree(workspace, instance)},
		{Source: BaseVolume(project, workspace), Target: containerBase(workspace)},
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

// Worktree is the worktree a git workspace needs for an instance.
type Worktree struct {
	Workspace string
	Instance  string
	Volume    string // the worktree's volume
	Base      string // the base's volume
	URL       string
	Branch    string
}

func (c Worktree) String() string {
	return fmt.Sprintf("workspace %s for %s into %s", c.Workspace, c.Instance, c.Volume)
}

// listing returns the agents that list a git workspace, sorted.
func listing(project *config.Resolved, workspace string) []string {
	var agents []string
	for name, agent := range project.Agents {
		for _, mount := range agent.Workspaces {
			if mount.Name == workspace {
				agents = append(agents, name)
				break
			}
		}
	}
	sort.Strings(agents)
	return agents
}

// PlanWorktrees lists the worktrees instance needs, one per git workspace it lists.
func PlanWorktrees(project *config.Resolved, instance string) []Worktree {
	var plan []Worktree
	agent, ok := project.Agents[instance]
	if !ok {
		return nil
	}
	for _, mount := range agent.Workspaces {
		ws, ok := project.Workspaces[mount.Name]
		if !ok || ws.Git == nil {
			continue
		}
		plan = append(plan, Worktree{
			Workspace: mount.Name, Instance: instance, Volume: WorktreeVolume(project.Name, mount.Name, instance),
			Base: BaseVolume(project.Name, mount.Name), URL: ws.Git.URL, Branch: ws.Git.Branch,
		})
	}
	return plan
}

// quietPrep is a prep container that works on volumes and has no network: nothing it does leaves the engine.
func quietPrep(env PrepEnv, project, dir string, cmd []string, mounts []MountSpec) PrepSpec {
	return PrepSpec{
		Image:    env.Image,
		Cmd:      cmd,
		User:     env.User,
		Env:      []string{"HOME=/tmp"},
		Mounts:   mounts,
		Identity: engine.Identity{Project: project, Service: "prep", Kind: "prep", ProjectDir: dir},
	}
}

// baseInfo asks the base clone of a workspace about itself, in a prep container: the volume cannot be read from
// here. A base that has no volume is not cloned, and says so without a container.
func baseInfo(ctx context.Context, c *engine.Client, env PrepEnv, project, dir, workspace string) (gitprep.BaseInfo, error) {
	none := gitprep.BaseInfo{Branches: []string{}}
	volume := BaseVolume(project, workspace)
	exists, err := volumeExists(ctx, c, volume)
	if err != nil || !exists {
		return none, err
	}
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return none, err
	}
	out, err := RunPrep(ctx, c, quietPrep(env, project, dir, []string{"/egzo", "prep", "git", "info", containerBase(workspace)},
		[]MountSpec{{Source: volume, Target: containerBase(workspace), ReadOnly: true}}))
	if err != nil {
		return none, fmt.Errorf("read the base clone of workspace %q: %w", workspace, err)
	}
	var info gitprep.BaseInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return none, fmt.Errorf("read the base clone of workspace %q: %w", workspace, err)
	}
	return info, nil
}

// CheckWorktrees refuses a plan that would take over what is already there: a worktree volume, or the branch of the
// instance in the base. Both are somebody's work, so only reuse continues with them.
func CheckWorktrees(ctx context.Context, c *engine.Client, env PrepEnv, project, dir string, plan []Worktree, reuse bool) error {
	if reuse {
		return nil
	}
	for _, worktree := range plan {
		exists, err := volumeExists(ctx, c, worktree.Volume)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("the volume %s already exists: it is the worktree of %q in workspace %q; pass --reuse to continue with it, "+
				"or remove it with `egzo workspace rm %s --worktree %s`", worktree.Volume, worktree.Instance, worktree.Workspace, worktree.Workspace, worktree.Instance)
		}
		info, err := baseInfo(ctx, c, env, project, dir, worktree.Workspace)
		if err != nil {
			return err
		}
		branch := gitprep.Branch(worktree.Instance)
		for _, have := range info.Branches {
			if have == branch {
				return fmt.Errorf("the branch %s already exists in the base clone of workspace %q, left by an earlier instance of that name: "+
					"pass --reuse to continue it, or pick another name", branch, worktree.Workspace)
			}
		}
	}
	return nil
}

// leftOverNames are the instance names that an earlier instance left a worktree volume or a branch under in the
// git workspaces: such a name is not free for an automatic name, which would fail on it.
func leftOverNames(ctx context.Context, c *engine.Client, env PrepEnv, observed Observed, project, dir string, workspaces map[string]config.ResolvedWorkspace) (map[string]bool, error) {
	left := map[string]bool{}
	for _, r := range observed.Resources {
		if r.Type == "volume" && r.Kind == kindWorkspace && r.Instance != "" {
			left[r.Instance] = true
		}
	}
	for _, name := range sortedKeys(workspaces) {
		if workspaces[name].Git == nil {
			continue
		}
		info, err := baseInfo(ctx, c, env, project, dir, name)
		if err != nil {
			return nil, err
		}
		for _, branch := range info.Branches {
			left[strings.TrimPrefix(branch, "egzo/")] = true
		}
	}
	return left, nil
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
// base is cloned (when it is missing) and fetched through the proxy under the instance's identity. A volume that
// this call made and that the job did not fill is removed again: an empty base or worktree is not work.
func RunWorktreePreps(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, env PrepEnv, from InstanceIdentity, tokens map[string]string, plan []Worktree, options WorktreeOptions, out io.Writer) error {
	if len(plan) == 0 {
		return nil
	}
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return err
	}
	var made []string // the volumes this call created, removed again when it fails: they hold no work yet
	fail := func(err error) error {
		for _, name := range made {
			c.API.VolumeRemove(context.WithoutCancel(ctx), name, true)
		}
		return err
	}
	for _, worktree := range plan {
		fmt.Fprintf(out, "worktree %s\n", worktree)
		madeBase, err := ensureOwnedVolume(ctx, c, env, worktree.Base, workspaceIdentity(project.Name, dir, worktree.Workspace, nil, ""))
		if err != nil {
			return err
		}
		treeIdentity := workspaceIdentity(project.Name, dir, worktree.Workspace, &from, worktree.Instance)
		madeTree, err := ensureOwnedVolume(ctx, c, env, worktree.Volume, treeIdentity)
		if err == nil && !madeTree {
			// a worktree volume that was there (--reuse): hand it to the agent user too, whoever made it
			err = chownVolumes(ctx, c, env.Image, env.User, []string{worktree.Volume}, treeIdentity)
		}
		if madeTree {
			made = append(made, worktree.Volume)
		}
		if err != nil {
			if madeBase {
				c.API.VolumeRemove(context.WithoutCancel(ctx), worktree.Base, true)
			}
			return fail(err)
		}
		cmd := []string{"/egzo", "prep", "git", "worktree", "--url", worktree.URL,
			"--base", containerBase(worktree.Workspace), "--agent", worktree.Instance}
		if worktree.Branch != "" {
			cmd = append(cmd, "--branch", worktree.Branch)
		}
		if options.NoFetch {
			cmd = append(cmd, "--no-fetch")
		}
		if options.Reuse {
			cmd = append(cmd, "--reuse")
		}
		cmd = append(cmd, containerWorktree(worktree.Workspace, worktree.Instance))
		stdout, err := runGit(ctx, c, project, dir, env, worktree.Instance, tokens[worktree.Instance],
			InstanceNetwork(project.Name, worktree.Instance), cmd, worktreeMounts(project.Name, worktree.Workspace, worktree.Instance))
		if err != nil {
			if madeBase {
				c.API.VolumeRemove(context.WithoutCancel(ctx), worktree.Base, true)
			}
			return fail(gitFailure(project, worktree.Instance, worktree.Workspace, worktree.URL, err))
		}
		for _, line := range strings.Split(stdout, "\n") {
			if warning, ok := strings.CutPrefix(line, warningPrefix); ok {
				fmt.Fprintf(out, "warning: workspace %s: %s\n", worktree.Workspace, warning)
			}
		}
	}
	return nil
}

// runGit runs one git job in a prep container on a network the proxy serves, as agent.
func runGit(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, env PrepEnv, agent, token, network string, cmd []string, mounts []MountSpec) (string, error) {
	vars := proxyEnv(project.Name, agent, token)
	vars["HOME"] = "/tmp"
	all := append([]MountSpec{{Source: project.Name + "_ca", Target: caAgentDir, ReadOnly: true}}, mounts...)
	return RunPrep(ctx, c, PrepSpec{
		Image:    env.Image,
		Cmd:      cmd,
		User:     env.User,
		Env:      envList(vars),
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
func CloneBases(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, env PrepEnv, out, warn io.Writer) {
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		agents := listing(project, name)
		if ws.Git == nil || len(agents) == 0 {
			continue
		}
		info, err := baseInfo(ctx, c, env, project.Name, dir, name)
		if err != nil {
			fmt.Fprintf(warn, "warning: workspace %q was not cloned: %v\n", name, err)
			continue
		}
		if info.Cloned {
			if info.URL != "" && info.URL != ws.Git.URL {
				fmt.Fprintf(warn, "warning: workspace %q: the base clone %s has the url %s, but egzo.yaml says %s: it is left as it is "+
					"(`egzo workspace rm %s` removes it, and the next `up` clones again)\n", name, BaseVolume(project.Name, name), info.URL, ws.Git.URL, name)
			}
			continue
		}
		fmt.Fprintf(out, "clone workspace %s into %s\n", name, BaseVolume(project.Name, name))
		if err := cloneBase(ctx, c, project, dir, env, name, ws, agents[0]); err != nil {
			fmt.Fprintf(warn, "warning: workspace %q was not cloned: %v\n", name, err)
		}
	}
}

func cloneBase(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, env PrepEnv, name string, ws config.ResolvedWorkspace, agent string) error {
	profile := project.Agents[agent].Egress
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return err
	}
	volume := BaseVolume(project.Name, name)
	identity := workspaceIdentity(project.Name, dir, name, nil, "")
	made, err := ensureOwnedVolume(ctx, c, env, volume, identity)
	if err == nil && !made {
		// an empty base that was there (a clone cut short, or a volume made by hand): hand it over too
		err = chownVolumes(ctx, c, env.Image, env.User, []string{volume}, identity)
	}
	if err != nil {
		return err
	}
	cmd := []string{"/egzo", "prep", "git", "clone", "--url", ws.Git.URL}
	if ws.Git.Branch != "" {
		cmd = append(cmd, "--branch", ws.Git.Branch)
	}
	cmd = append(cmd, containerBase(name))
	err = withPrepIdentity(ctx, c, project, dir, profile, func(network, token string) error {
		_, err := runGit(ctx, c, project, dir, env, prepIdentity, token, network, cmd,
			[]MountSpec{{Source: volume, Target: containerBase(name)}})
		return err
	})
	if err != nil {
		if made {
			c.API.VolumeRemove(context.WithoutCancel(ctx), volume, true)
		}
		return gitFailure(project, agent, name, ws.Git.URL, err)
	}
	return nil
}

// FetchBase makes the base clone of a git workspace when it is missing and fetches into it when it is there, through
// the proxy under the egress profile of the first template that lists the workspace. It is `workspace fetch`.
func FetchBase(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, env PrepEnv, name string, out io.Writer) error {
	ws, ok := project.Workspaces[name]
	if !ok || ws.Git == nil {
		return fmt.Errorf("workspace %q is not a git workspace", name)
	}
	agents := listing(project, name)
	if len(agents) == 0 {
		return fmt.Errorf("no agent lists workspace %q: nothing says which egress profile may reach %s", name, ws.Git.URL)
	}
	info, err := baseInfo(ctx, c, env, project.Name, dir, name)
	if err != nil {
		return err
	}
	volume := BaseVolume(project.Name, name)
	if !info.Cloned {
		fmt.Fprintf(out, "clone workspace %s into %s\n", name, volume)
		return cloneBase(ctx, c, project, dir, env, name, ws, agents[0])
	}
	fmt.Fprintf(out, "fetch workspace %s into %s\n", name, volume)
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return err
	}
	cmd := []string{"/egzo", "prep", "git", "fetch", "--url", ws.Git.URL}
	if ws.Git.Branch != "" {
		cmd = append(cmd, "--branch", ws.Git.Branch)
	}
	cmd = append(cmd, containerBase(name))
	err = withPrepIdentity(ctx, c, project, dir, project.Agents[agents[0]].Egress, func(network, token string) error {
		_, err := runGit(ctx, c, project, dir, env, prepIdentity, token, network, cmd,
			[]MountSpec{{Source: volume, Target: containerBase(name)}})
		return err
	})
	if err != nil {
		return gitFailure(project, agents[0], name, ws.Git.URL, err)
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

// baseWorkspaces lists the git workspaces that have a base clone, from the volumes observed.
func baseWorkspaces(observed Observed) []string {
	var names []string
	for _, r := range observed.Resources {
		if r.Type == "volume" && r.Kind == kindWorkspace && r.Instance == "" && strings.HasSuffix(r.Name, ".base") {
			names = append(names, r.Workspace)
		}
	}
	sort.Strings(names)
	return names
}

// ForgetWorktrees makes the base clones forget the worktrees of instances whose volumes are gone, so that a name
// can be used again. The branches stay: what was committed outlives the instance. It is best effort: a base that
// cannot be reached has nothing the next spawn of the name cannot cope with (`--reuse` forces).
func ForgetWorktrees(ctx context.Context, c *engine.Client, env PrepEnv, project, dir string, observed Observed, instances []string) error {
	if len(instances) == 0 {
		return nil
	}
	bases := baseWorkspaces(observed)
	if len(bases) == 0 {
		return nil
	}
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return err
	}
	var failed []string
	for _, workspace := range bases {
		cmd := append([]string{"/egzo", "prep", "git", "forget", "--base", containerBase(workspace)}, instances...)
		spec := quietPrep(env, project, dir, cmd, []MountSpec{{Source: BaseVolume(project, workspace), Target: containerBase(workspace)}})
		if _, err := RunPrep(ctx, c, spec); err != nil {
			failed = append(failed, fmt.Sprintf("workspace %s: %v", workspace, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not make the base clones forget the removed worktrees: %s", strings.Join(failed, "; "))
	}
	return nil
}
