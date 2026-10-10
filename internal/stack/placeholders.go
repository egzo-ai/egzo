// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// placeholderStandIn is what a dry run compares with while a placeholder is not made yet. It is
// not a placeholder anything holds.
const placeholderStandIn = "egzo-ph-00000000000000000000000000000000"

// PlaceholderRefs lists, sorted, the secret references that a placeholder service of a profile some
// agent uses names.
func PlaceholderRefs(project *config.Resolved) []string {
	seen := map[string]bool{}
	for _, name := range usedProfiles(project) {
		for _, service := range project.Egress[name].Services {
			if service.Placeholder != "" && service.Secret != "" {
				seen[service.Secret] = true
			}
		}
	}
	return sortedKeys(seen)
}

// withStandIns completes a map of placeholders with the stand-in for every reference it lacks.
func withStandIns(refs []string, have map[string]string) map[string]string {
	out := map[string]string{}
	for _, ref := range refs {
		if have[ref] != "" {
			out[ref] = have[ref]
		} else {
			out[ref] = placeholderStandIn
		}
	}
	return out
}

// ReadPlaceholders returns the placeholders the control sidecar holds for the references, and the
// stand-in for the ones it does not (or when it cannot be asked). It creates nothing: it is what a dry
// run and the staleness check use.
func ReadPlaceholders(ctx context.Context, c *engine.Client, project *config.Resolved) map[string]string {
	refs := PlaceholderRefs(project)
	if len(refs) == 0 {
		return nil
	}
	have := map[string]string{}
	if result, err := c.Exec(ctx, project.Name+"-control-1", []string{"/egzo", "control", "request", "GET", "/placeholders"}, nil); err == nil && result.ExitCode == 0 {
		_ = json.Unmarshal(result.Stdout, &have)
	}
	return withStandIns(refs, have)
}

// EnsurePlaceholders returns the placeholder of every reference, which the control sidecar makes the
// first time and keeps for as long as its volume lives.
func EnsurePlaceholders(ctx context.Context, c *engine.Client, project *config.Resolved) (map[string]string, error) {
	refs := PlaceholderRefs(project)
	if len(refs) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(refs)
	result, err := c.Exec(ctx, project.Name+"-control-1", []string{"/egzo", "control", "request", "POST", "/placeholders"}, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("get the placeholders: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("get the placeholders: %s", strings.TrimSpace(string(result.Stderr)))
	}
	have := map[string]string{}
	if err := json.Unmarshal(result.Stdout, &have); err != nil {
		return nil, fmt.Errorf("get the placeholders: %w", err)
	}
	for _, ref := range refs {
		if have[ref] == "" {
			return nil, fmt.Errorf("get the placeholders: none for secret %s", ref)
		}
	}
	return have, nil
}

// templatePlaceholders is what a spawn puts in an agent's environment: variable to placeholder text.
func templatePlaceholders(profile *config.ResolvedProfile, placeholders map[string]string) map[string]string {
	if profile == nil {
		return nil
	}
	out := map[string]string{}
	for _, service := range profile.Services {
		if service.Placeholder == "" || service.Secret == "" {
			continue
		}
		text := placeholders[service.Secret]
		if text == "" {
			text = placeholderStandIn
		}
		out[service.Placeholder] = text
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
