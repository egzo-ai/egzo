// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/egzo-ai/egzo/internal/access"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/names"
)

// AccessOf is what an instance of the template is told about what it may reach: its profile, the hosts it allows, each
// service and how to use it, the ssh servers. It names no secret and no vault: a service says only what kind of
// credential the proxy gives it.
func AccessOf(template Template) access.Access {
	out := access.Access{Profile: template.Agent.Egress, Allow: []string{}, OtherHosts: access.OtherHosts, Services: []access.Service{}, SSH: []string{}}
	profile := template.Profile
	if profile == nil {
		return out
	}
	out.Allow = append(out.Allow, profile.Allow...)
	for _, name := range sortedKeys(profile.Services) {
		service := profile.Services[name]
		entry := access.Service{Name: name, Hosts: append([]string{}, service.Hosts...), Paths: service.Paths, Inspect: service.Inspect, Credential: access.None}
		switch {
		case service.OAuth():
			entry.Credential = access.SignIn
		case service.Placeholder != "" && service.SecretRef() != "":
			entry.Credential, entry.Variable = access.Placeholder, service.Placeholder
		case service.Inject != nil && service.SecretRef() != "":
			entry.Credential = access.Injected
		}
		entry.How = access.How(name, entry.Credential, entry.Variable)
		out.Services = append(out.Services, entry)
	}
	out.SSH = append(out.SSH, sortedKeys(profile.SSH)...)
	return out
}

// pushAccess gives the control sidecar what each of the instances may reach, in one request. The sidecar keeps it for
// the agent's `access`, and tells a running agent whose access it already had, and that changed, what changed.
func pushAccess(ctx context.Context, c *engine.Client, project string, instances []Instance, p Published) error {
	body := map[string]access.Access{}
	for _, instance := range instances {
		if template, ok := p.Templates[instance.Template]; ok && instance.State != "missing" {
			body[instance.Name] = AccessOf(template)
		}
	}
	if len(body) == 0 {
		return nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if _, err := ControlRequest(ctx, c, project, "PUT", "/v1/access", data); err != nil {
		return fmt.Errorf("tell control what the agents may reach: %w", err)
	}
	return nil
}

// ControlPushAccess gives the control sidecar what one new instance of the template may reach.
func ControlPushAccess(ctx context.Context, c *engine.Client, project, name string, template Template) error {
	data, err := json.Marshal(map[string]access.Access{name: AccessOf(template)})
	if err != nil {
		return err
	}
	if _, err := ControlRequest(ctx, c, project, "PUT", "/v1/access", data); err != nil {
		return fmt.Errorf("tell control what %s may reach: %w", name, err)
	}
	return nil
}

// identity is what other agents may know of an instance besides its name, its template and its harness, and what it knows of itself: who
// started it. `up` does not know the starter's name: control keeps the one spawn gave.
type identity struct {
	Template    string `json:"template"`
	Harness     string `json:"harness"`
	Starter     string `json:"starter,omitempty"`
	StarterName string `json:"starter_name,omitempty"`
}

// pushIdentities gives the control sidecar the template and harness of each instance, in one request, for `agents`.
func pushIdentities(ctx context.Context, c *engine.Client, project string, instances []Instance, p Published) error {
	body := map[string]identity{}
	for _, instance := range instances {
		if template, ok := p.Templates[instance.Template]; ok && instance.State != "missing" {
			body[instance.Name] = identity{Template: instance.Template, Harness: template.Agent.Harness, Starter: starterOf(instance.Actor)}
		}
	}
	if len(body) == 0 {
		return nil
	}
	return putIdentities(ctx, c, project, body)
}

// ControlPushIdentity gives the control sidecar the template and harness of one new instance, and who started it.
func ControlPushIdentity(ctx context.Context, c *engine.Client, project, name, templateName string, template Template, from InstanceIdentity) error {
	return putIdentities(ctx, c, project, map[string]identity{name: {Template: templateName, Harness: template.Agent.Harness,
		Starter: starterOf(from.Actor), StarterName: from.StarterName}})
}

// starterOf is who started an instance: its actor label, the operator when it has none.
func starterOf(actor string) string {
	if actor == "" {
		return names.Operator
	}
	return actor
}

func putIdentities(ctx context.Context, c *engine.Client, project string, body map[string]identity) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if _, err := ControlRequest(ctx, c, project, "PUT", "/v1/identities", data); err != nil {
		return fmt.Errorf("tell control the agents' templates: %w", err)
	}
	return nil
}
