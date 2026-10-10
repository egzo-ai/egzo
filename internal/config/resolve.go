// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/docker/go-units"
)

// Resolved is the fully resolved project: what `egzo config` prints.
type Resolved struct {
	// Secrets maps "vault/name" to where that secret is read from. It is never printed.
	Secrets    map[string]SecretRef         `yaml:"-"`
	Name       string                       `yaml:"name"`
	Vaults     map[string][]string          `yaml:"vaults,omitempty"`
	Workspaces map[string]ResolvedWorkspace `yaml:"workspaces"`
	Agents     map[string]ResolvedAgent     `yaml:"agents"`
	Egress     map[string]*ResolvedProfile  `yaml:"egress"`
	Proxy      *ResolvedProxy               `yaml:"proxy,omitempty"`
}

// SecretRef is one secret of a vault: the vault it is listed in, its name there and that vault's backend.
type SecretRef struct {
	Vault, Name, Backend string
}

// ResolvedProxy is what the file says about the proxy sidecar; nil when it says nothing.
type ResolvedProxy struct {
	Image string `yaml:"image"`
}

type ResolvedAgent struct {
	Harness     string            `yaml:"harness"`
	Image       string            `yaml:"image,omitempty"`
	Workdir     string            `yaml:"workdir"`
	Egress      string            `yaml:"egress"`
	Workspaces  []Mount           `yaml:"workspaces"`
	Model       string            `yaml:"model,omitempty"`
	Prompt      string            `yaml:"prompt,omitempty"`
	Resources   Resources         `yaml:"resources,omitempty"`
	Runtime     string            `yaml:"runtime,omitempty"`
	Permissions string            `yaml:"permissions"`
	Env         map[string]string `yaml:"env,omitempty"`
	Inject      ResolvedInject    `yaml:"inject"`
}

// ResolvedInject is how messages reach the agent's terminal, with defaults applied.
type ResolvedInject struct {
	HumanQuiet string `yaml:"human_quiet"`
	AckTimeout string `yaml:"ack_timeout"`
	IdleSignal string `yaml:"idle_signal"`
	Quiescence string `yaml:"quiescence"`
}

// harnessesWithHooks report their own state through hooks; the others are watched by their output.
var harnessesWithHooks = map[string]bool{"claude-code": true, "opencode": true}

func resolveInject(name string, agent Agent, p *problems) ResolvedInject {
	inject := ResolvedInject{HumanQuiet: "30s", AckTimeout: "60s", IdleSignal: "quiescence", Quiescence: "5s"}
	if harnessesWithHooks[agent.Harness] {
		inject.IdleSignal = "hook"
	}
	if agent.Inject == nil {
		return inject
	}
	duration := func(key, value string, target *string) {
		if value == "" {
			return
		}
		if parsed, err := time.ParseDuration(value); err != nil || parsed <= 0 {
			p.addf("agent %q: inject.%s %q is not a positive duration like 30s or 2m", name, key, value)
			return
		}
		*target = value
	}
	duration("human_quiet", agent.Inject.HumanQuiet, &inject.HumanQuiet)
	duration("ack_timeout", agent.Inject.AckTimeout, &inject.AckTimeout)
	duration("quiescence", agent.Inject.Quiescence, &inject.Quiescence)
	switch agent.Inject.IdleSignal {
	case "":
	case "hook", "quiescence":
		inject.IdleSignal = agent.Inject.IdleSignal
	default:
		p.addf("agent %q: inject.idle_signal %q is not hook or quiescence", name, agent.Inject.IdleSignal)
	}
	return inject
}

func checkResources(agent string, r Resources, p *problems) {
	if r.CPUs < 0 {
		p.addf("agent %q: resources.cpus must not be negative", agent)
	}
	if r.Pids < 0 {
		p.addf("agent %q: resources.pids must not be negative", agent)
	}
	if r.Memory != "" {
		if bytes, err := units.RAMInBytes(r.Memory); err != nil || bytes <= 0 {
			p.addf("agent %q: resources.memory %q is not a size like 512m or 4g", agent, r.Memory)
		}
	}
}

// Resolve validates the file and returns the resolved project plus non-fatal warnings. dir is the
// project directory, name the already resolved project name.
func Resolve(file *File, name, dir string) (*Resolved, []string, error) {
	p := &problems{}

	if file.Version != 0 && file.Version != 1 {
		p.addf("unsupported version %d (this egzo understands version 1)", file.Version)
	}
	checkVaults(file.Vaults, p)
	checkNames(file, p)

	resolved := &Resolved{
		Name:       name,
		Vaults:     map[string][]string{},
		Workspaces: resolveWorkspaces(file, dir, p),
		Agents:     map[string]ResolvedAgent{},
		Egress:     resolveEgress(file, p),
	}
	if file.Proxy.Image != "" {
		resolved.Proxy = &ResolvedProxy{Image: file.Proxy.Image}
	}
	resolved.Secrets = map[string]SecretRef{}
	for vault, definition := range file.Vaults {
		names := slices.Clone(definition.Secrets)
		for _, secret := range names {
			resolved.Secrets[vault+"/"+secret] = SecretRef{Vault: vault, Name: secret, Backend: definition.Backend}
		}
		sort.Strings(names)
		resolved.Vaults[vault] = names
	}

	refs := agentRefs(file, p)
	agentNames := make([]string, 0, len(file.Agents))
	for agentName := range file.Agents {
		agentNames = append(agentNames, agentName)
	}
	sort.Strings(agentNames)

	var warnings []string
	for _, agentName := range agentNames {
		agent := file.Agents[agentName]
		resolvedAgent, agentWarnings := resolveAgent(file, agentName, agent, dir, resolved, refs, p)
		resolved.Agents[agentName] = resolvedAgent
		warnings = append(warnings, agentWarnings...)
	}

	if err := p.err(); err != nil {
		return nil, nil, err
	}
	return resolved, warnings, nil
}

func resolveAgent(
	file *File, name string, agent Agent, dir string, resolved *Resolved, refs map[string][]ref, p *problems,
) (ResolvedAgent, []string) {
	var warnings []string

	switch {
	case agent.Harness == "":
		p.addf("agent %q: harness is required (one of: %s)", name, strings.Join(HarnessNames(), ", "))
	default:
		if _, ok := harnesses[agent.Harness]; !ok {
			p.addf("agent %q: unknown harness %q (one of: %s)", name, agent.Harness, strings.Join(HarnessNames(), ", "))
		}
	}
	if agent.Harness == "custom" && agent.Image == "" {
		p.addf("agent %q: the custom harness needs an image", name)
	}
	if agent.Permissions != "" && agent.Permissions != "bypass" && agent.Permissions != "default" {
		p.addf("agent %q: unknown permissions %q (use bypass or default)", name, agent.Permissions)
	}
	checkEnv(name, agent.Env, p)
	if agent.Prompt != "" {
		path := agent.Prompt
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			p.addf("agent %q: prompt %q is not an existing file (%s)", name, agent.Prompt, path)
		}
	}

	profileName := agent.Egress
	if profileName == "" {
		profileName = defaultProfile
	}
	profile, ok := resolved.Egress[profileName]
	if !ok {
		p.addf("agent %q: egress profile %q is not defined under egress:", name, profileName)
	} else if hosts, known := harnesses[agent.Harness]; known {
		for _, host := range hosts {
			if !profile.reaches(host) {
				warnings = append(warnings, fmt.Sprintf(
					"warning: agent %q cannot reach %s with egress profile %q, which %s needs",
					name, host, profileName, agent.Harness))
			}
		}
	}

	if profile != nil {
		for _, serviceName := range sortedServices(profile) {
			if placeholder := profile.Services[serviceName].Placeholder; placeholder != "" {
				if _, set := agent.Env[placeholder]; set {
					p.addf("agent %q: env %s is also the placeholder variable of service %q in egress profile %q", name, placeholder, serviceName, profileName)
				}
			}
		}
	}

	if agent.Harness == "opencode" {
		if profile != nil {
			for serviceName, service := range profile.Services {
				if service.Inject != nil && strings.EqualFold(service.Inject.Header, "Authorization") && slices.Contains(service.Hosts, "api.anthropic.com") {
					warnings = append(warnings, fmt.Sprintf(
						"warning: agent %q runs opencode, which sends its Anthropic key as x-api-key; service %q injects a bearer token (a subscription token), which opencode cannot use: bind an API key to the anthropic service",
						name, serviceName))
				}
			}
		}
	}
	mounts, workdir := resolveMounts(file, name, agent, dir, resolved.Workspaces, refs, p)
	for _, mount := range mounts {
		if mount.HostPath != "" {
			if reason := socketWarning(mount.HostPath); reason != "" {
				warnings = append(warnings, fmt.Sprintf("warning: agent %q mounts %s", name, reason))
			}
		}
	}
	checkResources(name, agent.Resources, p)

	permissions := agent.Permissions
	if permissions == "" {
		permissions = "bypass"
	}
	return ResolvedAgent{
		Harness:     agent.Harness,
		Image:       agent.Image,
		Workdir:     workdir,
		Egress:      profileName,
		Workspaces:  mounts,
		Model:       agent.Model,
		Prompt:      agent.Prompt,
		Resources:   agent.Resources,
		Runtime:     agent.Runtime,
		Permissions: permissions,
		Env:         agent.Env,
		Inject:      resolveInject(name, agent, p),
	}, warnings
}

// vaultBackends are the backends that exist.
var vaultBackends = []string{"env", "pass"}

func checkVaults(vaults map[string]Vault, p *problems) {
	names := make([]string, 0, len(vaults))
	for name := range vaults {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		vault := vaults[name]
		if strings.Contains(name, "/") {
			p.addf("vault %q: names cannot contain '/'", name)
		}
		switch {
		case vault.Backend == "":
			p.addf("vault %q: backend is required (backends: %s)", name, strings.Join(vaultBackends, ", "))
			continue
		case !slices.Contains(vaultBackends, vault.Backend):
			p.addf("vault %q: unknown backend %q (backends: %s)", name, vault.Backend, strings.Join(vaultBackends, ", "))
			continue
		}
		seen := map[string]bool{}
		for _, secret := range vault.Secrets {
			if seen[secret] {
				p.addf("vault %q: secret %q is listed twice", name, secret)
			}
			seen[secret] = true
			if reason := secretNameProblem(vault.Backend, secret); reason != "" {
				p.addf("vault %q: secret name %q %s", name, secret, reason)
			}
		}
	}
}

// secretNameProblem says why name is not a secret name for the backend, or returns "".
func secretNameProblem(backend, name string) string {
	if backend == "env" {
		if !envName.MatchString(name) {
			return "is not an environment variable name (letters, digits and '_', not starting with a digit)"
		}
		return ""
	}
	if strings.HasPrefix(name, "-") {
		return "cannot start with '-' (pass would read it as an option)"
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return "cannot contain a control character"
		}
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "cannot start or end with '/'"
	}
	for _, segment := range strings.Split(name, "/") {
		switch segment {
		case "":
			return "cannot have an empty segment"
		case ".", "..":
			return "cannot have a '.' or '..' segment"
		}
	}
	return ""
}

var (
	secretValuePatterns = []*regexp.Regexp{
		regexp.MustCompile(`^sk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`^gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`^xox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`^AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`^AIza[0-9A-Za-z_-]{30,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	}
	sensitiveKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|credential)`)
	opaqueValue  = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]{16,}$`)
)

// checkEnv rejects env values that look like secrets: those belong in a vault. The message names
// the variable and never the value.
func checkEnv(agent string, env map[string]string, p *problems) {
	names := make([]string, 0, len(env))
	for key := range env {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		value := env[key]
		if reservedEnvName(key) {
			p.addf("agent %q: env %s is set by egzo to wire the agent to its sidecars and cannot be overridden", agent, key)
			continue
		}
		looksSecret := sensitiveKey.MatchString(key) && opaqueValue.MatchString(value)
		for _, pattern := range secretValuePatterns {
			looksSecret = looksSecret || pattern.MatchString(value)
		}
		if looksSecret {
			p.addf("agent %q: env %s looks like a secret; put it in a vault and bind it through an egress profile", agent, key)
		}
	}
}

func sortedServices(profile *ResolvedProfile) []string {
	names := serviceKeys(profile)
	sort.Strings(names)
	return names
}
