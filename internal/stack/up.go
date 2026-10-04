package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

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
func AgentUser(c *engine.Client) string {
	if c.Podman || os.Getuid() < 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

func (o Options) inputs(c *engine.Client, tokens map[string]string) Inputs {
	return Inputs{Image: o.Image, Tokens: tokens, User: AgentUser(c), HarnessPrefix: o.HarnessPrefix}
}

// Up converges a project: control first, because it hands out the per-agent tokens everything
// else needs, then every other resource, then the proxy's policy.
func Up(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, opts Options, out io.Writer) error {
	observed, err := Observe(ctx, c, project.Name)
	if err != nil {
		return err
	}
	if err := CheckOwnership(observed, project.Name, dir); err != nil {
		return err
	}

	// Read the secrets first: an agent must not start without the credentials its profile
	// promises, and nothing should be half-created when one is missing.
	var secrets map[string]string
	if !opts.DryRun {
		if secrets, err = ResolveSecrets(project, dir); err != nil {
			return err
		}
	}

	fresh := map[string]bool{} // resources created by this run: nothing can be stored in them yet
	tokens := map[string]string{}
	if len(project.Agents) > 0 {
		tokens, observed, err = agentTokens(ctx, c, project, dir, opts, observed, fresh, out)
		if err != nil {
			return err
		}
	}

	desired, err := Desire(project, dir, opts.inputs(c, tokens))
	if err != nil {
		return err
	}
	warnRuntimes(c, desired)
	checkouts, err := PlanGit(project)
	if err != nil {
		return err
	}
	plan := BuildPlan(desired, observed, opts.Recreate)
	switch {
	case opts.DryRun:
		if err := Apply(ctx, c, desired, plan, true, out); err != nil {
			return err
		}
		for _, checkout := range checkouts {
			fmt.Fprintf(out, "would clone %s\n", checkout)
		}
	case len(checkouts) > 0:
		// The clones go through the proxy as the agent, so the sidecars, the agent's network and the
		// policy must be in place before the agent's container, which needs the clone, exists.
		sidecars, agents := splitAgentContainers(plan, project)
		if err := Apply(ctx, c, desired, sidecars, false, out); err != nil {
			return err
		}
		markFresh(fresh, sidecars)
		if _, err := pushPolicy(ctx, c, project, desired, tokens, secrets, opts, fresh[desired.Proxy], out); err != nil {
			return err
		}
		fresh[desired.Proxy] = false // it has its policy now
		if err := RunGitPreps(ctx, c, project, dir, opts.Image, AgentUser(c), tokens, checkouts, out); err != nil {
			return err
		}
		if err := Apply(ctx, c, desired, agents, false, out); err != nil {
			return err
		}
		markFresh(fresh, agents)
	default:
		if err := Apply(ctx, c, desired, plan, false, out); err != nil {
			return err
		}
		markFresh(fresh, plan)
	}

	// The proxy and the control sidecar are independent: talk to them at the same time.
	out = &lockedWriter{w: out}
	var pushed, stored bool
	var pushErr, storeErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pushed, pushErr = pushPolicy(ctx, c, project, desired, tokens, secrets, opts, fresh[desired.Proxy], out)
	}()
	go func() {
		defer wg.Done()
		if !opts.DryRun {
			stored, storeErr = pushSnapshot(ctx, c, project, desired, fresh[project.Name+"_control"], out)
		}
	}()
	wg.Wait()
	if pushErr != nil {
		return pushErr
	}
	if storeErr != nil {
		return storeErr
	}
	if len(plan) == 0 && len(checkouts) == 0 && !pushed && !stored {
		fmt.Fprintln(out, "nothing to do")
	}
	return nil
}

// agentTokens makes sure the control sidecar runs, then asks it for each agent's token. In a dry
// run nothing is created, so agents get a placeholder when control is not there yet.
func agentTokens(
	ctx context.Context, c *engine.Client, project *config.Resolved, dir string, opts Options, observed Observed, fresh map[string]bool, out io.Writer,
) (map[string]string, Observed, error) {
	controlName := project.Name + "-control-1"
	control := observed.find("container", controlName)
	if control == nil || control.State != "running" {
		if opts.DryRun {
			tokens := map[string]string{}
			for name := range project.Agents {
				tokens[name] = "(assigned by the control sidecar)"
			}
			return tokens, observed, nil
		}
		bootstrap, err := Desire(project, dir, opts.inputs(c, nil))
		if err != nil {
			return nil, observed, err
		}
		// Everything but the agents can come up now: only the agents need the tokens, and the
		// proxy starts while control does.
		agentOwned := map[string]bool{}
		for name := range project.Agents {
			agentOwned[project.Name+"_"+name] = true
			agentOwned[project.Name+"-"+name+"-1"] = true
		}
		var sidecars []Action
		for _, action := range BuildPlan(bootstrap, observed, false) {
			if !agentOwned[action.Name] && action.Verb != "connect" {
				sidecars = append(sidecars, action)
			}
		}
		if err := Apply(ctx, c, bootstrap, sidecars, false, out); err != nil {
			return nil, observed, err
		}
		markFresh(fresh, sidecars)
		if observed, err = Observe(ctx, c, project.Name); err != nil {
			return nil, observed, err
		}
	}

	names := sortedKeys(project.Agents)
	values := make([]string, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
			return nil, observed, errs[i]
		}
		tokens[name] = values[i]
	}
	return tokens, observed, nil
}

// pushPolicy loads the egress policy into the proxy when the proxy does not already have it. The
// policy carries secret values, so it goes through exec stdin and lives only in the proxy's memory.
func pushPolicy(
	ctx context.Context, c *engine.Client, project *config.Resolved, desired Desired,
	tokens, secrets map[string]string, opts Options, fresh bool, out io.Writer,
) (bool, error) {
	if desired.Proxy == "" {
		return false, nil
	}
	if opts.DryRun {
		observed, err := Observe(ctx, c, project.Name)
		if err != nil {
			return false, err
		}
		if proxy := observed.find("container", desired.Proxy); proxy == nil || proxy.State != "running" {
			fmt.Fprintln(out, "would load the egress policy into the proxy")
			return true, nil
		}
		fmt.Fprintln(out, "would check the egress policy loaded in the proxy")
		return false, nil
	}

	policy := BuildPolicy(project, tokens, secrets)

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

// warnRuntimes says what Podman cannot do: its Docker-compatible API ignores the OCI runtime a
// container asks for, so the runtime is neither applied nor verifiable from here. Compose has the
// same limit; unlike Compose we say so, because the runtime is usually a security boundary.
func warnRuntimes(c *engine.Client, desired Desired) {
	if !c.Podman {
		return
	}
	for _, spec := range desired.Containers {
		if spec.Runtime != "" {
			fmt.Fprintf(os.Stderr, "warning: %s asks for the %s runtime, but Podman's Docker-compatible API ignores "+
				"the runtime, so egzo can neither apply nor verify it. Set it as the default in containers.conf "+
				"(runtime = %q), or use Docker\n", spec.Name, spec.Runtime, spec.Runtime)
		}
	}
}

// splitAgentContainers separates what must exist before an agent's container (networks, volumes,
// the sidecars) from the agent containers themselves.
func splitAgentContainers(plan []Action, project *config.Resolved) (before, agents []Action) {
	agentContainers := map[string]bool{}
	for name := range project.Agents {
		agentContainers[project.Name+"-"+name+"-1"] = true
	}
	for _, action := range plan {
		if action.Type == "container" && agentContainers[action.Name] {
			agents = append(agents, action)
		} else {
			before = append(before, action)
		}
	}
	return before, agents
}
