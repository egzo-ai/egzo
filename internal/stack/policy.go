// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// BuildPolicy turns the egress profiles agents use into the policy the proxy enforces, one entry per
// profile. It embeds secret values, so it must only travel through exec stdin, never a label,
// environment variable or file visible to `inspect`. Which agent uses which profile is a matter of
// bindings (see BindingFor), which carry no secret.
func BuildPolicy(project *config.Resolved, secrets, placeholders map[string]string) proxy.Policy {
	policy := proxy.Policy{Profiles: map[string]proxy.Profile{}}
	for _, name := range usedProfiles(project) {
		profile := project.Egress[name]
		entry := proxy.Profile{Allow: append([]string{}, profile.Allow...)}
		for _, serviceName := range sortedKeys(profile.Services) {
			service := profile.Services[serviceName]
			service2 := proxy.Service{Name: serviceName, Hosts: service.Hosts, Secret: secrets[service.Secret], Inspect: service.Inspect}
			if service.Inject != nil {
				service2.Header, service2.Value = service.Inject.Header, service.Inject.Value
			}
			if service.Placeholder != "" {
				service2.Placeholder = placeholders[service.Secret]
				if service2.Placeholder == "" {
					service2.Placeholder = placeholderStandIn
				}
			}
			entry.Services = append(entry.Services, service2)
		}
		policy.Profiles[name] = entry
	}
	data, _ := json.Marshal(policy)
	sum := sha256.Sum256(data)
	policy.Hash = hex.EncodeToString(sum[:])
	return policy
}

// usedProfiles lists, sorted, the egress profiles some agent of the project runs under.
func usedProfiles(project *config.Resolved) []string {
	seen := map[string]bool{}
	for _, agent := range project.Agents {
		if project.Egress[agent.Egress] != nil {
			seen[agent.Egress] = true
		}
	}
	return sortedKeys(seen)
}

// BindingFor is what the proxy needs to let an instance through: its credentials and the profile of
// its template. It holds no secret.
func BindingFor(token, profile string) proxy.Binding {
	return proxy.Binding{Token: token, Profile: profile}
}

// ResolveSecrets reads the value of every secret an agent's profile injects. A secret that cannot
// be read is an error: an agent must not start without the credentials its profile promises.
func ResolveSecrets(project *config.Resolved, dir string) (map[string]string, error) {
	needed := map[string]bool{}
	for _, agent := range project.Agents {
		for _, service := range project.Egress[agent.Egress].Services {
			if (service.Inject != nil || service.Placeholder != "") && service.Secret != "" {
				needed[service.Secret] = true
			}
		}
	}
	values := map[string]string{}
	var problems []string
	refs := make([]string, 0, len(needed))
	for ref := range needed {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		value, err := ReadSecret(project.Secrets[ref], dir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("secret %s: %v", ref, err))
			continue
		}
		values[ref] = value
	}
	problems = append(problems, checkCredentialKinds(project, values)...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return values, nil
}

// checkCredentialKinds catches the one mix-up that is certain to fail and is easy to make: a Claude
// subscription token (`claude setup-token`, sk-ant-oat...) is not an API key. api.anthropic.com takes the
// token only as `Authorization: Bearer` and a key only as x-api-key; sent the other way it answers 401
// "API key is invalid". The message never contains the value.
func checkCredentialKinds(project *config.Resolved, values map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, agent := range project.Agents {
		for name, service := range project.Egress[agent.Egress].Services {
			if service.Inject == nil || service.Secret == "" || !contains(service.Hosts, "api.anthropic.com") || seen[name+service.Secret] {
				continue
			}
			seen[name+service.Secret] = true
			value := values[service.Secret]
			bearer := strings.EqualFold(service.Inject.Header, "Authorization")
			switch {
			case !bearer && strings.HasPrefix(value, "sk-ant-oat"):
				problems = append(problems, fmt.Sprintf("secret %s is a Claude subscription (OAuth) token, but service %q sends it as %s, "+
					"which api.anthropic.com only accepts for API keys: bind the secret to the anthropic-oauth service instead", service.Secret, name, service.Inject.Header))
			case bearer && strings.HasPrefix(value, "sk-ant-api"):
				problems = append(problems, fmt.Sprintf("secret %s is an Anthropic API key, but service %q sends it as a bearer token, "+
					"which api.anthropic.com only accepts for subscription tokens: bind the secret to the anthropic service instead", service.Secret, name))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
