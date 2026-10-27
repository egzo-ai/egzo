// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/harness"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// signInEnv is what a new instance is given of the sign-ins of its profile's OAuth services, for a harness that keeps
// its tokens in a file of its own: Codex's auth.json, made of the stand-ins of the project's sign-in. Nothing when the
// harness has no such file, the profile no OAuth service, or the proxy no live sign-in: the client then asks to sign in.
func signInEnv(ctx context.Context, c *engine.Client, project string, view *config.Resolved, name string) map[string]string {
	agent, ok := view.Agents[name] // the view of a spawn has the instance under its own name
	if !ok || agent.Harness != "codex" {
		return nil
	}
	profile := view.Egress[agent.Egress]
	if profile == nil {
		return nil
	}
	var services []string
	for name, service := range profile.Services {
		if service.OAuth() {
			services = append(services, name)
		}
	}
	sort.Strings(services)
	for _, service := range services {
		result, err := c.Exec(ctx, project+"-proxy-1", []string{"/egzo", "proxy", "request", "GET", "/oauth/" + service + "/stand-ins"}, nil)
		if err != nil || result.ExitCode != 0 {
			continue
		}
		var standIns proxy.StandIns
		if json.Unmarshal(result.Stdout, &standIns) != nil || standIns.Access == "" {
			continue
		}
		auth := harness.CodexAuth(standIns.Access, standIns.Refresh, standIns.ID, standIns.Refreshed.UTC().Format(time.RFC3339))
		return map[string]string{harness.EnvCodexAuth: auth}
	}
	return nil
}
