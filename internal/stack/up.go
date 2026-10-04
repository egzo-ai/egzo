package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Options are the flags of `egzo up`.
type Options struct {
	DryRun   bool
	Recreate bool
	Image    string
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

	tokens := map[string]string{}
	if len(project.Agents) > 0 {
		tokens, observed, err = agentTokens(ctx, c, project, dir, opts, observed, out)
		if err != nil {
			return err
		}
	}

	desired, err := Desire(project, dir, Inputs{Image: opts.Image, Tokens: tokens})
	if err != nil {
		return err
	}
	plan := BuildPlan(desired, observed, opts.Recreate)
	if err := Apply(ctx, c, desired, plan, opts.DryRun, out); err != nil {
		return err
	}

	pushed, err := pushPolicy(ctx, c, project, desired, tokens, secrets, opts, out)
	if err != nil {
		return err
	}
	stored := false
	if !opts.DryRun {
		if stored, err = pushSnapshot(ctx, c, project, desired, out); err != nil {
			return err
		}
	}
	if len(plan) == 0 && !pushed && !stored {
		fmt.Fprintln(out, "nothing to do")
	}
	return nil
}

// agentTokens makes sure the control sidecar runs, then asks it for each agent's token. In a dry
// run nothing is created, so agents get a placeholder when control is not there yet.
func agentTokens(
	ctx context.Context, c *engine.Client, project *config.Resolved, dir string, opts Options, observed Observed, out io.Writer,
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
		bootstrap, err := Desire(project, dir, Inputs{Image: opts.Image})
		if err != nil {
			return nil, observed, err
		}
		var control []Action
		for _, action := range BuildPlan(bootstrap, observed, false) {
			if strings.Contains(action.Name, "_control") || action.Name == controlName {
				control = append(control, action)
			}
		}
		if err := Apply(ctx, c, bootstrap, control, false, out); err != nil {
			return nil, observed, err
		}
		if observed, err = Observe(ctx, c, project.Name); err != nil {
			return nil, observed, err
		}
	}

	tokens := map[string]string{}
	for name := range project.Agents {
		result, err := c.Exec(ctx, controlName, []string{"/egzo", "control", "request", "GET", "/tokens/" + name}, nil)
		if err != nil {
			return nil, observed, fmt.Errorf("token for agent %q: %w", name, err)
		}
		if result.ExitCode != 0 {
			return nil, observed, fmt.Errorf("token for agent %q: %s", name, strings.TrimSpace(string(result.Stderr)))
		}
		tokens[name] = strings.TrimSpace(string(result.Stdout))
	}
	return tokens, observed, nil
}

// pushPolicy loads the egress policy into the proxy when the proxy does not already have it. The
// policy carries secret values, so it goes through exec stdin and lives only in the proxy's memory.
func pushPolicy(
	ctx context.Context, c *engine.Client, project *config.Resolved, desired Desired,
	tokens, secrets map[string]string, opts Options, out io.Writer,
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
