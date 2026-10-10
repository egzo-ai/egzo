// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Options are the flags of `egzo up`.
type Options struct {
	DryRun   bool
	Recreate bool
	Image    string
	// HarnessPrefix overrides where harness images are pulled from (EGZO_HARNESS_PREFIX).
	HarnessPrefix string
}

// AgentUser is who agents run as: the invoking user, so files they write on the host are theirs.
// Podman maps users itself (rootless container root is the invoking user), so it keeps the image's.
func AgentUser(c *engine.Client) string { return agentUser(c, os.Getuid(), os.Getgid()) }

// agentUser is the "uid:gid" agents run as. An engine that maps users itself (Podman, rootless Docker)
// keeps the image's user. Root is never an agent's user: someone running egzo as root (sudo) still gets
// agents that run unprivileged.
func agentUser(c *engine.Client, uid, gid int) string {
	switch {
	case c.Podman || c.Rootless || uid < 0:
		return ""
	case uid == 0:
		return "1000:1000"
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

// Up converges a project's infrastructure: the sidecars, their networks and volumes, the proxy's
// policy, and the published templates. It starts no agent: instances are spawned from the templates.
// It refuses while an instance is stale, because converging would leave it running something the file no
// longer says.
func Up(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, opts Options, out io.Writer) error {
	observed, err := Observe(ctx, c, project.Name)
	if err != nil {
		return err
	}
	if err := CheckOwnership(observed, project.Name, dir); err != nil {
		return err
	}
	// What the placeholders are so far: a dry run, and the staleness check before anything is changed,
	// use the stand-in for one that is not made yet.
	placeholders := ReadPlaceholders(ctx, c, project)
	published, err := PublishWith(project, dir, opts.Image, opts.HarnessPrefix, AgentUser(c), placeholders)
	if err != nil {
		return err
	}
	if stale := StaleInstances(observed, published); len(stale) > 0 {
		if !opts.DryRun {
			return StaleError(stale)
		}
		for _, instance := range stale {
			fmt.Fprintf(out, "stale: %s\n", instance.Name)
		}
		fmt.Fprintln(out, "up would refuse until the stale instances are removed")
	}

	// Read the secrets first: nothing should be half-created when one is missing.
	var secrets map[string]string
	if !opts.DryRun {
		if secrets, err = ResolveSecrets(project, dir); err != nil {
			return err
		}
	} else if resolved, err := ResolveSecrets(project, dir); err == nil {
		secrets = resolved // a dry run compares the policy too, when the secrets can be read
	}

	fresh := map[string]bool{} // resources created by this run: nothing can be stored in them yet
	desired, err := Desire(project, dir, Inputs{Image: opts.Image, User: AgentUser(c), HarnessPrefix: opts.HarnessPrefix, Instances: observed.InstanceNames()})
	if err != nil {
		return err
	}
	plan := BuildPlan(desired, observed, opts.Recreate)
	if err := Apply(ctx, c, desired, plan, opts.DryRun, out); err != nil {
		return err
	}
	markFresh(fresh, plan)
	if !opts.DryRun && len(placeholders) > 0 {
		// The control sidecar exists now: it makes the placeholders that are missing, once.
		if placeholders, err = EnsurePlaceholders(ctx, c, project); err != nil {
			return err
		}
		if published, err = PublishWith(project, dir, opts.Image, opts.HarnessPrefix, AgentUser(c), placeholders); err != nil {
			return err
		}
	}

	out = &lockedWriter{w: out}
	var pushed, stored, published2 bool
	var pushErr, storeErr, publishErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		pushed, pushErr = pushPolicy(ctx, c, project, desired, secrets, placeholders, opts, fresh[desired.Proxy], out)
		if pushErr == nil && !opts.DryRun && desired.Proxy != "" {
			// A proxy that was created, started again or restarted has no bindings: the instances that
			// exist and are not bound get theirs back.
			pushErr = bindMissing(ctx, c, project.Name, observed.Instances(), published)
		}
	}()
	go func() {
		defer wg.Done()
		if !opts.DryRun {
			stored, storeErr = pushSnapshot(ctx, c, project, desired, fresh[project.Name+"_control"], out)
		}
	}()
	go func() {
		defer wg.Done()
		if opts.DryRun {
			published2 = dryRunTemplates(ctx, c, desired.Control, published, out)
			return
		}
		published2, publishErr = publishTemplates(ctx, c, published, desired.Control, fresh[desired.Control], out)
	}()
	wg.Wait()
	for _, err := range []error{pushErr, storeErr, publishErr} {
		if err != nil {
			return err
		}
	}
	if len(plan) == 0 && !pushed && !stored && !published2 {
		fmt.Fprintln(out, "nothing to do")
	}
	return nil
}

// dryRunTemplates reports whether `up` would publish different templates than the control sidecar holds.
func dryRunTemplates(ctx context.Context, c *engine.Client, controlName string, p Published, out io.Writer) bool {
	data, err := p.Marshal()
	if err != nil {
		return false
	}
	current, err := c.Exec(ctx, controlName, []string{"/egzo", "control", "request", "GET", "/templates"}, nil)
	if err == nil && current.ExitCode == 0 && bytes.Equal(bytes.TrimSpace(current.Stdout), data) {
		return false
	}
	fmt.Fprintln(out, "would publish templates "+strings.Join(p.Names(), ", "))
	return true
}

// bindMissing binds the instances the proxy does not know. The proxy keeps its bindings in memory, so
// whatever made it start again took them.
func bindMissing(ctx context.Context, c *engine.Client, project string, instances []Instance, p Published) error {
	if len(instances) == 0 {
		return nil
	}
	result, err := c.Exec(ctx, project+"-proxy-1", []string{"/egzo", "proxy", "request", "GET", "/agents"}, nil)
	if err != nil {
		return fmt.Errorf("list the agents bound in the proxy: %w", err)
	}
	var bound []string
	if result.ExitCode == 0 {
		_ = json.Unmarshal(result.Stdout, &bound)
	}
	var missing []Instance
	for _, instance := range instances {
		if instance.State != "missing" && !slices.Contains(bound, instance.Name) {
			missing = append(missing, instance)
		}
	}
	return BindInstances(ctx, c, project, missing, p)
}

// BindInstances gives the proxy the credentials of the instances, bound to their templates' profiles.
// It binds every instance it can and reports each one it could not.
func BindInstances(ctx context.Context, c *engine.Client, project string, instances []Instance, p Published) error {
	var bindable []Instance
	var names []string
	for _, instance := range instances {
		// a stale instance whose template is gone stays denied; what is left of a removed one has no
		// registration and needs nothing
		if _, ok := p.Templates[instance.Template]; ok && instance.State != "missing" {
			bindable = append(bindable, instance)
			names = append(names, instance.Name)
		}
	}
	if len(bindable) == 0 {
		return nil
	}
	tokens, err := fetchTokens(ctx, c, project+"-control-1", names)
	if err != nil {
		return err
	}
	var failures []error
	for _, instance := range bindable {
		if err := bindProxy(ctx, c, project, instance.Name, tokens[instance.Name], p.Templates[instance.Template].Agent.Egress); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// fetchTokens asks the control sidecar for the token of each agent, all at once.
func fetchTokens(ctx context.Context, c *engine.Client, controlName string, names []string) (map[string]string, error) {
	values := make([]string, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			result, err := c.Exec(ctx, controlName, []string{"/egzo", "control", "request", "GET", "/tokens/" + name}, nil)
			switch {
			case err != nil:
				errs[i] = fmt.Errorf("token for agent %q: %w", name, err)
			case result.ExitCode != 0:
				errs[i] = fmt.Errorf("token for agent %q: %s", name, strings.TrimSpace(string(result.Stderr)))
			default:
				values[i] = strings.TrimSpace(string(result.Stdout))
			}
		}()
	}
	wg.Wait()
	tokens := map[string]string{}
	for i, name := range names {
		if errs[i] != nil {
			return nil, errs[i]
		}
		tokens[name] = values[i]
	}
	return tokens, nil
}

// bindProxy lets an instance through the proxy under a profile: its token and the profile's name, never
// a secret.
func bindProxy(ctx context.Context, c *engine.Client, project, name, token, profile string) error {
	body, _ := json.Marshal(BindingFor(token, profile))
	result, err := c.Exec(ctx, project+"-proxy-1", []string{"/egzo", "proxy", "request", "PUT", "/agents/" + name}, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bind %s in the proxy: %w", name, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("bind %s in the proxy: %s", name, strings.TrimSpace(string(result.Stderr)))
	}
	return nil
}

func unbindProxy(ctx context.Context, c *engine.Client, project, name string) error {
	result, err := c.Exec(ctx, project+"-proxy-1", []string{"/egzo", "proxy", "request", "DELETE", "/agents/" + name}, nil)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(result.Stderr)))
	}
	return nil
}

func registerControl(ctx context.Context, c *engine.Client, project, name string) error {
	_, err := ControlRequest(ctx, c, project, "PUT", "/agents/"+name, nil)
	return err
}

func unregisterControl(ctx context.Context, c *engine.Client, project, name string) error {
	_, err := ControlRequest(ctx, c, project, "DELETE", "/agents/"+name, nil)
	return err
}

// ReloadPolicy loads the egress policy into a proxy that was started again, and binds the instances
// again: the policy and the bindings live in the proxy's memory, so a restarted proxy refuses everything
// until it is given them. The policy comes from the published templates, as spawn does, so a file edited
// since `up` changes nothing for the instances that run; only the secrets' locations come from the file.
// The proxy needs a moment before its operator API answers, so this retries for a while.
func ReloadPolicy(ctx context.Context, c *engine.Client, project *config.Resolved, dir string) error {
	published, err := ReadPublished(ctx, c, project.Name)
	source, notPublished := project, errors.Is(err, ErrNotPublished)
	if err == nil {
		source = published.AsProject(project.Secrets)
	} else if !notPublished {
		return err
	}
	secrets, err := ResolveSecrets(source, dir)
	if err != nil {
		return err
	}
	placeholders, err := EnsurePlaceholders(ctx, c, source)
	if err != nil {
		return err
	}
	body, err := json.Marshal(BuildPolicy(source, secrets, placeholders))
	if err != nil {
		return err
	}
	proxyName := project.Name + "-proxy-1"
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := c.Exec(ctx, proxyName, []string{"/egzo", "proxy", "request", "PUT", "/policy"}, bytes.NewReader(body))
		if err == nil && result.ExitCode == 0 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err == nil {
				err = fmt.Errorf("%s", strings.TrimSpace(string(result.Stderr)))
			}
			return fmt.Errorf("load proxy policy: %w", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if notPublished {
		return nil
	}
	observed, err := Observe(ctx, c, project.Name)
	if err != nil {
		return err
	}
	return BindInstances(ctx, c, project.Name, observed.Instances(), published)
}

// pushPolicy loads the egress policy into the proxy when the proxy does not already have it. The
// policy carries secret values, so it goes through exec stdin and lives only in the proxy's memory.
func pushPolicy(
	ctx context.Context, c *engine.Client, project *config.Resolved, desired Desired,
	secrets, placeholders map[string]string, opts Options, fresh bool, out io.Writer,
) (bool, error) {
	if desired.Proxy == "" {
		return false, nil
	}
	if opts.DryRun {
		observed, err := Observe(ctx, c, project.Name)
		if err != nil {
			return false, err
		}
		proxy := observed.find("container", desired.Proxy)
		if proxy == nil || proxy.State != "running" {
			fmt.Fprintln(out, "would load the egress policy into the proxy")
			return true, nil
		}
		if secrets != nil {
			policy := BuildPolicy(project, secrets, placeholders)
			loaded, err := c.Exec(ctx, desired.Proxy, []string{"/egzo", "proxy", "request", "GET", "/policy"}, nil)
			var current struct {
				Hash string `json:"hash"`
			}
			if err == nil && loaded.ExitCode == 0 {
				_ = json.Unmarshal(loaded.Stdout, &current)
			}
			if current.Hash != policy.Hash {
				fmt.Fprintln(out, "would load the egress policy into the proxy")
				return true, nil
			}
		}
		fmt.Fprintln(out, "would check the egress policy loaded in the proxy")
		return false, nil
	}

	policy := BuildPolicy(project, secrets, placeholders)

	// The policy lives in the proxy's memory only: a proxy this run just created has none.
	if !fresh {
		loaded, err := c.Exec(ctx, desired.Proxy, []string{"/egzo", "proxy", "request", "GET", "/policy"}, nil)
		if err != nil {
			return false, fmt.Errorf("query proxy policy: %w", err)
		}
		var current struct {
			Hash string `json:"hash"`
		}
		if loaded.ExitCode == 0 {
			_ = json.Unmarshal(loaded.Stdout, &current)
		}
		if current.Hash == policy.Hash {
			return false, nil
		}
	}

	body, err := json.Marshal(policy)
	if err != nil {
		return false, err
	}
	fmt.Fprintln(out, "load egress policy into proxy")
	result, err := c.Exec(ctx, desired.Proxy, []string{"/egzo", "proxy", "request", "PUT", "/policy"}, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("load proxy policy: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("load proxy policy: %s", strings.TrimSpace(string(result.Stderr)))
	}
	return true, nil
}

// markFresh records the containers and volumes a plan creates.
func markFresh(fresh map[string]bool, plan []Action) {
	for _, action := range plan {
		if action.Verb == "create" && (action.Type == "container" || action.Type == "volume") {
			fresh[action.Name] = true
		}
	}
}

// refuseRuntime stops a spawn that asks for an OCI runtime on Podman. Podman's Docker-compatible
// API cannot select a runtime, so egzo could neither apply nor verify it, and the runtime is
// usually a security boundary (gVisor): starting the agent without it would be a silent downgrade.
func refuseRuntime(c *engine.Client, agent, runtime string) error {
	if !c.Podman || runtime == "" {
		return nil
	}
	return fmt.Errorf("agent %q asks for the %s runtime, but Podman's Docker-compatible API cannot select a runtime, "+
		"so egzo can neither apply nor verify it and will not start the agent without it: "+
		"remove runtime: from the agent, or use Docker with the runtime registered", agent, runtime)
}
