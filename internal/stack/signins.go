// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"slices"
	"sort"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/harness"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// signInEnv is what a new instance is given of the sign-in its harness uses, when the harness's client keeps one itself
// (harness.SignsIn): Codex's auth.json, or the refresh token Claude Code signs in with, made of the stand-ins of the
// project's sign-in. Nothing when the profile has no OAuth service for the harness's host or the proxy no live sign-in:
// the client then asks to sign in.
func signInEnv(ctx context.Context, c *engine.Client, project string, view *config.Resolved, name string) map[string]string {
	agent, ok := view.Agents[name] // the view of a spawn has the instance under its own name
	if !ok {
		return nil
	}
	integration, ok := harness.For(agent.Harness)
	if !ok {
		return nil
	}
	signsIn, ok := integration.(harness.SignsIn)
	if !ok {
		return nil
	}
	for _, service := range signInServices(view.Egress[agent.Egress], agent.Harness) {
		result, err := c.Exec(ctx, project+"-proxy-1", []string{"/egzo", "proxy", "request", "GET", "/oauth/" + service + "/stand-ins"}, nil)
		if err != nil || result.ExitCode != 0 {
			continue
		}
		var standIns proxy.StandIns
		if json.Unmarshal(result.Stdout, &standIns) != nil || standIns.Access == "" {
			continue
		}
		return signsIn.SignInEnv(harness.SignIn{Access: standIns.Access, Refresh: standIns.Refresh, ID: standIns.ID, Scope: standIns.Scope,
			Refreshed: standIns.Refreshed})
	}
	return nil
}

// signInServices lists, sorted, the OAuth services of a profile whose sign-in a harness's client keeps itself (harness.SignsIn):
// the ones that cover its sign-in host, or any when it names none. A new instance is given the stand-ins of the first one signed in.
func signInServices(profile *config.ResolvedProfile, harnessName string) []string {
	integration, ok := harness.For(harnessName)
	if !ok || profile == nil {
		return nil
	}
	signsIn, ok := integration.(harness.SignsIn)
	if !ok {
		return nil
	}
	var services []string
	for name, service := range profile.Services {
		if host := signsIn.SignInHost(); service.OAuth() && (host == "" || slices.Contains(service.Hosts, host)) {
			services = append(services, name)
		}
	}
	sort.Strings(services)
	return services
}
