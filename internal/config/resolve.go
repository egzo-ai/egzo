package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Resolved is the fully resolved project: what `egzo config` prints.
type Resolved struct {
	// SecretSources maps "vault/SECRET" to its from: source. It is never printed.
	SecretSources map[string]string            `yaml:"-"`
	Runtime       Runtime                      `yaml:"runtime,omitempty"`
	Name          string                       `yaml:"name"`
	Vaults        map[string][]string          `yaml:"vaults,omitempty"`
	Workspaces    map[string]ResolvedWorkspace `yaml:"workspaces"`
	Agents        map[string]ResolvedAgent     `yaml:"agents"`
	Egress        map[string]*ResolvedProfile  `yaml:"egress"`
}

type ResolvedAgent struct {
	Harness     string            `yaml:"harness"`
	Image       string            `yaml:"image,omitempty"`
	Workdir     string            `yaml:"workdir"`
	Egress      string            `yaml:"egress"`
	Workspaces  []Mount           `yaml:"workspaces"`
	Model       string            `yaml:"model,omitempty"`
	Prompt      string            `yaml:"prompt,omitempty"`
	Tools       []string          `yaml:"tools,omitempty"`
	Resources   Resources         `yaml:"resources,omitempty"`
	Isolation   string            `yaml:"isolation,omitempty"`
	Permissions string            `yaml:"permissions"`
	Env         map[string]string `yaml:"env,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
}

// Resolve validates the file and returns the resolved project plus non-fatal warnings. dir is the
// project directory, name the already resolved project name.
func Resolve(file *File, name, dir string) (*Resolved, []string, error) {
	p := &problems{}

	if file.Version != 0 && file.Version != 1 {
		p.addf("unsupported version %d (this egzo understands version 1)", file.Version)
	}
	checkRuntime(file.Runtime, p)
	checkVaults(file.Vaults, p)

	resolved := &Resolved{
		Name:       name,
		Runtime:    file.Runtime,
		Vaults:     map[string][]string{},
		Workspaces: resolveWorkspaces(file, dir, p),
		Agents:     map[string]ResolvedAgent{},
		Egress:     resolveEgress(file, p),
	}
	resolved.SecretSources = map[string]string{}
	for vault, definition := range file.Vaults {
		names := make([]string, 0, len(definition.Secrets))
		for secret, source := range definition.Secrets {
			names = append(names, secret)
			resolved.SecretSources[vault+"/"+secret] = source.From
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

	if strings.ContainsAny(name, "/:") {
		p.addf("agent %q: names cannot contain '/' or ':'", name)
	}
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
	if !validIsolation(agent.Isolation) {
		p.addf("agent %q: unknown isolation %q (use rootless, gvisor or default)", name, agent.Isolation)
	}
	for _, dependency := range agent.DependsOn {
		if !hasAgent(file, dependency) {
			p.addf("agent %q: depends_on names unknown agent %q", name, dependency)
		}
	}
	checkEnv(name, agent.Env, p)

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

	mounts, workdir := resolveMounts(file, name, agent, dir, resolved.Workspaces, refs, p)

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
		Tools:       agent.Tools,
		Resources:   agent.Resources,
		Isolation:   agent.Isolation,
		Permissions: permissions,
		Env:         agent.Env,
		DependsOn:   agent.DependsOn,
	}, warnings
}

func checkRuntime(runtime Runtime, p *problems) {
	if runtime.Engine != "" && runtime.Engine != "auto" && runtime.Engine != "docker" && runtime.Engine != "podman" {
		p.addf("runtime: unknown engine %q (use auto, docker or podman)", runtime.Engine)
	}
	if !validIsolation(runtime.Isolation) {
		p.addf("runtime: unknown isolation %q (use rootless, gvisor or default)", runtime.Isolation)
	}
	if runtime.Network != "" && runtime.Network != "isolated" {
		p.addf("runtime: unknown network %q (only isolated is supported)", runtime.Network)
	}
}

func validIsolation(value string) bool {
	return value == "" || value == "rootless" || value == "gvisor" || value == "default"
}

var vaultBackends = map[string]bool{"env": true, "file": true, "sops": true, "pass": true, "1password": true}

// secretSchemes are the sources `from:` can read today.
var secretSchemes = []string{"env", "file"}

func checkVaults(vaults map[string]Vault, p *problems) {
	for name, vault := range vaults {
		if strings.Contains(name, "/") {
			p.addf("vault %q: names cannot contain '/'", name)
		}
		if vault.Backend != "" && !vaultBackends[vault.Backend] {
			p.addf("vault %q: unknown backend %q", name, vault.Backend)
		}
		for secret, source := range vault.Secrets {
			if strings.Contains(secret, "/") {
				p.addf("vault %q: secret name %q cannot contain '/'", name, secret)
			}
			scheme, rest, found := strings.Cut(source.From, ":")
			supported := false
			for _, s := range secretSchemes {
				supported = supported || s == scheme
			}
			switch {
			case !found || rest == "":
				p.addf("vault %q: secret %q needs from: <scheme>:<location> (schemes: %s)", name, secret, strings.Join(secretSchemes, ", "))
			case !supported:
				p.addf("vault %q: secret %q: unsupported source scheme %q (schemes: %s)", name, secret, scheme, strings.Join(secretSchemes, ", "))
			}
		}
	}
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
		looksSecret := sensitiveKey.MatchString(key) && opaqueValue.MatchString(value)
		for _, pattern := range secretValuePatterns {
			looksSecret = looksSecret || pattern.MatchString(value)
		}
		if looksSecret {
			p.addf("agent %q: env %s looks like a secret; put it in a vault and bind it through an egress profile", agent, key)
		}
	}
}
