// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"

	"github.com/egzo-ai/egzo/internal/names"
)

// Resolved is the fully resolved project: what `egzo project check --resolved` prints.
type Resolved struct {
	// Secrets maps "vault/name" to where that secret is read from. It is never printed.
	Secrets map[string]SecretRef `yaml:"-"`
	Name    string               `yaml:"name"`
	// Hub is the hub the project links to, when it names one.
	Hub *ResolvedHub `yaml:"hub,omitempty"`
	// LocalPorts is set when the project turns local ports on.
	LocalPorts *ResolvedLocalPorts `yaml:"local_ports,omitempty"`
	// SecretKey is the reference of the project's secret key, which seals what egzo keeps at rest.
	SecretKey  string                       `yaml:"secret_key"`
	Vaults     map[string]ResolvedVault     `yaml:"vaults,omitempty"`
	Workspaces map[string]ResolvedWorkspace `yaml:"workspaces"`
	Agents     map[string]ResolvedAgent     `yaml:"agents"`
	Egress     map[string]*ResolvedProfile  `yaml:"egress"`
	Proxy      *ResolvedProxy               `yaml:"proxy,omitempty"`
}

// UnusedVaults are the warnings `project check` gives about the vaults nothing refers to. They are not warnings
// of Resolve: every command would print them, and only the check is about the file.
func (r *Resolved) UnusedVaults() []string {
	var warnings []string
	for _, vault := range slices.Sorted(maps.Keys(r.Vaults)) {
		if len(r.Vaults[vault].Secrets) == 0 {
			warnings = append(warnings, fmt.Sprintf("warning: vault %q: nothing refers to a secret in it", vault))
		}
	}
	return warnings
}

// ResolvedVault is a vault and the names of the secrets the file refers to in it, sorted.
type ResolvedVault struct {
	Backend string   `yaml:"backend"`
	Secrets []string `yaml:"secrets"`
}

// SecretRef is one secret of a vault: the vault a reference names, its name there and that vault's backend.
type SecretRef struct {
	Vault, Name, Backend string
}

// ResolvedProxy is what the file says about the proxy sidecar; nil when it says nothing.
// ResolvedHub is a hub's address, without a trailing slash, and the path of the CA of its certificate.
type ResolvedHub struct {
	URL string `yaml:"url"`
	CA  string `yaml:"ca,omitempty"`
}

type ResolvedProxy struct {
	Image    string    `yaml:"image,omitempty"`
	Upstream *Upstream `yaml:"upstream,omitempty"`
}

type ResolvedAgent struct {
	Harness     string            `yaml:"harness"`
	Image       string            `yaml:"image,omitempty"`
	Build       *ResolvedBuild    `yaml:"build,omitempty"`
	Workdir     string            `yaml:"workdir"`
	Egress      string            `yaml:"egress"`
	Workspaces  []Mount           `yaml:"workspaces"`
	Model       string            `yaml:"model,omitempty"`
	Prompt      string            `yaml:"prompt,omitempty"`
	Resources   Resources         `yaml:"resources,omitempty"`
	Permissions string            `yaml:"permissions"`
	Env         map[string]string `yaml:"env,omitempty"`
	Inject      ResolvedInject    `yaml:"inject"`
	// Ports are the declared ports by name; nil when the template declares none.
	Ports map[string]ResolvedPort `yaml:"ports,omitempty"`
}

// ResolvedPort is a declared port: its number and its protocol (http, tcp or udp).
type ResolvedPort struct {
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

// String is the form the file writes, PORT/PROTOCOL.
func (r ResolvedPort) String() string { return fmt.Sprintf("%d/%s", r.Port, r.Protocol) }

// ResolvedLocalPorts is local_ports with its defaults applied.
type ResolvedLocalPorts struct {
	Bind       string `yaml:"bind" json:"bind"`
	Host       string `yaml:"host" json:"host"`
	PoolLow    int    `yaml:"pool_low" json:"pool_low"`
	PoolHigh   int    `yaml:"pool_high" json:"pool_high"`
	MaxExposed int    `yaml:"max_exposed" json:"max_exposed"`
}

// Defaults of local_ports.
const (
	DefaultPoolLow    = 20000
	DefaultPoolHigh   = 29999
	DefaultMaxExposed = 10
)

// MaxPorts is the most ports a template declares: the bound that keeps the status lines small.
const MaxPorts = 32

var portName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Port protocols.
const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
)

// ParsePort reads PORT/PROTOCOL.
func ParsePort(raw string) (ResolvedPort, bool) {
	number, protocol, ok := strings.Cut(raw, "/")
	if !ok || (protocol != ProtocolHTTP && protocol != ProtocolTCP && protocol != ProtocolUDP) {
		return ResolvedPort{}, false
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != number {
		return ResolvedPort{}, false
	}
	return ResolvedPort{Port: n, Protocol: protocol}, true
}

// resolveLocalPorts checks local_ports and applies its defaults.
func resolveLocalPorts(local *LocalPorts, p *problems) *ResolvedLocalPorts {
	if local == nil {
		return nil
	}
	resolved := &ResolvedLocalPorts{Bind: local.Bind, Host: local.Host, PoolLow: DefaultPoolLow, PoolHigh: DefaultPoolHigh, MaxExposed: DefaultMaxExposed}
	ip := net.ParseIP(local.Bind)
	switch {
	case local.Bind == "":
		p.addf("local_ports: bind is required: the IP address to listen on, such as 127.0.0.1")
	case ip == nil:
		p.addf("local_ports: bind %q is not an IP address", local.Bind)
	case ip.IsUnspecified() && local.Host == "":
		p.addf("local_ports: host is required when bind is %s: the name or address people use to reach this machine", local.Bind)
	}
	if resolved.Host == "" {
		resolved.Host = local.Bind
	}
	if local.Pool != "" {
		low, high, ok := parsePool(local.Pool)
		if !ok {
			p.addf("local_ports: pool %q is not a range like 20000-29999 within 1 to 65535", local.Pool)
		} else {
			resolved.PoolLow, resolved.PoolHigh = low, high
		}
	}
	if local.MaxExposed != nil {
		if *local.MaxExposed < 1 {
			p.addf("local_ports: max_exposed must be at least 1")
		} else {
			resolved.MaxExposed = *local.MaxExposed
		}
	}
	return resolved
}

func parsePool(raw string) (low, high int, ok bool) {
	a, b, found := strings.Cut(raw, "-")
	if !found {
		return 0, 0, false
	}
	low, err1 := strconv.Atoi(a)
	high, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || low < 1 || high > 65535 || low > high {
		return 0, 0, false
	}
	return low, high, true
}

// resolvePorts checks the ports an agent declares. The checks that need the project (local_ports, hub) are made here
// too: a port that could never open is refused when the file is read.
func resolvePorts(agent string, ports map[string]string, resolved *Resolved, p *problems) map[string]ResolvedPort {
	if len(ports) == 0 {
		return nil
	}
	if len(ports) > MaxPorts {
		p.addf("agent %q: %d ports are declared, at most %d are allowed", agent, len(ports), MaxPorts)
	}
	out := map[string]ResolvedPort{}
	byNumber := map[int][]bool{} // for each number, whether each port seen is udp
	raw := 0                       // tcp and udp ports, which a relay opens
	for _, name := range slices.Sorted(maps.Keys(ports)) {
		if len(name) > 32 || !portName.MatchString(name) {
			p.addf("agent %q: port name %q must start with a lowercase letter, hold only lowercase letters, digits and dashes, and be at most 32 characters", agent, name)
			continue
		}
		port, ok := ParsePort(ports[name])
		if !ok {
			p.addf("agent %q: port %q is %q, not PORT/PROTOCOL: a number from 1 to 65535, then http, tcp or udp, like 5173/http", agent, name, ports[name])
			continue
		}
		out[name] = port
		// http runs over tcp, so it takes the tcp side of a number
		if slices.Contains(byNumber[port.Port], port.Protocol == ProtocolUDP) {
			p.addf("agent %q: port %d is declared twice: only one tcp and one udp port can share a number", agent, port.Port)
		}
		byNumber[port.Port] = append(byNumber[port.Port], port.Protocol == ProtocolUDP)
		if port.Protocol != ProtocolHTTP {
			raw++
			if resolved.LocalPorts == nil {
				p.addf("agent %q: port %q is %s, which opens only as a local port: add local_ports to the file", agent, name, port.Protocol)
			}
		} else if resolved.Hub == nil && resolved.LocalPorts == nil {
			p.addf("agent %q: port %q is http and could never open: name a hub, or add local_ports to the file", agent, name)
		}
	}
	if resolved.LocalPorts != nil && raw > resolved.LocalPorts.MaxExposed {
		p.addf("agent %q: %d tcp and udp ports are declared but local_ports.max_exposed is %d: they could not all be open at once", agent, raw, resolved.LocalPorts.MaxExposed)
	}
	return out
}

// ResolvedBuild is an agent's build with its defaults: the context as the file wrote it (relative to the project
// file) and the Dockerfile relative to the context.
type ResolvedBuild struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	Args       map[string]string `yaml:"args,omitempty"`
	Target     string            `yaml:"target,omitempty"`
}

// HarnessImageArg is the build argument that carries the stock image of the agent's harness, at the CLI's version.
const HarnessImageArg = "EGZO_HARNESS_IMAGE"

// ResolvedInject is how messages reach the agent's terminal, with defaults applied.
type ResolvedInject struct {
	HumanQuiet string `yaml:"human_quiet"`
	AckTimeout string `yaml:"ack_timeout"`
	IdleSignal string `yaml:"idle_signal"`
	Quiescence string `yaml:"quiescence"`
}

// harnessesThatReportState report their own state (`egzo state`); the others are watched by their output.
var harnessesThatReportState = map[string]bool{"claude-code": true, "opencode": true}

func resolveInject(name string, agent Agent, p *problems) ResolvedInject {
	inject := ResolvedInject{HumanQuiet: "30s", AckTimeout: "60s", IdleSignal: "quiescence", Quiescence: "5s"}
	if harnessesThatReportState[agent.Harness] {
		inject.IdleSignal = "state"
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
	case "state", "quiescence":
		inject.IdleSignal = agent.Inject.IdleSignal
	default:
		p.addf("agent %q: inject.idle_signal %q is not state or quiescence", name, agent.Inject.IdleSignal)
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

// MaxPromptBytes is the largest prompt file: the text goes into the published template and an environment variable.
const MaxPromptBytes = 64 * 1024

// Resolve validates the file and returns the resolved project plus non-fatal warnings. dir is the
// project directory, name the already resolved project name.
func Resolve(file *File, name, dir string) (*Resolved, []string, error) {
	p := &problems{}

	if file.Version != 0 && file.Version != 1 {
		p.addf("unsupported version %d (this egzo understands version 1)", file.Version)
	}
	checkVaults(file.Vaults, p)
	checkNames(file, p)

	egress, secrets := resolveEgress(file, p)
	checkSecretKey(file, secrets, p)
	resolved := &Resolved{
		Name:       name,
		SecretKey:  file.SecretKey,
		Secrets:    secrets,
		Vaults:     map[string]ResolvedVault{},
		Workspaces: resolveWorkspaces(file, p),
		Agents:     map[string]ResolvedAgent{},
		Egress:     egress,
	}
	var warnings []string
	resolved.Hub, warnings = resolveHub(file.Hub, dir, p)
	resolved.LocalPorts = resolveLocalPorts(file.LocalPorts, p)
	if file.Proxy.Image != "" || file.Proxy.Upstream != nil {
		resolved.Proxy = &ResolvedProxy{Image: file.Proxy.Image, Upstream: resolveUpstream(file.Proxy.Upstream, dir, p)}
	}
	for vault, definition := range file.Vaults {
		resolved.Vaults[vault] = ResolvedVault{Backend: definition.Backend, Secrets: []string{}}
	}
	for _, secret := range secrets {
		vault := resolved.Vaults[secret.Vault]
		vault.Secrets = append(vault.Secrets, secret.Name)
		resolved.Vaults[secret.Vault] = vault
	}

	refs := agentRefs(file, p)
	agentNames := make([]string, 0, len(file.Agents))
	for agentName := range file.Agents {
		agentNames = append(agentNames, agentName)
	}
	sort.Strings(agentNames)

	for _, vault := range resolved.Vaults {
		sort.Strings(vault.Secrets)
	}
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
	if agent.Harness == "custom" && agent.Image == "" && agent.Build == nil {
		p.addf("agent %q: the custom harness needs an image or a build", name)
	}
	build := resolveBuild(name, agent, dir, p)
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
		} else if info.Size() > MaxPromptBytes {
			p.addf("agent %q: prompt %q is %d bytes, over the limit of 64 KB: the prompt travels in the published template", name, agent.Prompt, info.Size())
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
		if agent.Harness == "codex" && !profile.reaches("chatgpt.com") && !profile.reaches("api.openai.com") {
			warnings = append(warnings, fmt.Sprintf(
				"warning: agent %q cannot reach chatgpt.com or api.openai.com with egress profile %q: codex needs one, the chatgpt service for a ChatGPT sign-in or the openai service for an API key",
				name, profileName))
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
	mounts, workdir := resolveMounts(name, agent, resolved.Workspaces, refs, p)
	checkResources(name, agent.Resources, p)

	permissions := agent.Permissions
	if permissions == "" {
		permissions = "bypass"
	}
	return ResolvedAgent{
		Harness:     agent.Harness,
		Image:       agent.Image,
		Build:       build,
		Workdir:     workdir,
		Egress:      profileName,
		Workspaces:  mounts,
		Model:       agent.Model,
		Prompt:      agent.Prompt,
		Resources:   agent.Resources,
		Permissions: permissions,
		Env:         agent.Env,
		Inject:      resolveInject(name, agent, p),
		Ports:       resolvePorts(name, agent.Ports, resolved, p),
	}, warnings
}

// resolveBuild applies the defaults of an agent's build and checks that its context and Dockerfile exist, the
// Dockerfile inside the context, where the engine reads it.
func resolveBuild(name string, agent Agent, dir string, p *problems) *ResolvedBuild {
	if agent.Build == nil {
		return nil
	}
	if agent.Image != "" {
		p.addf("agent %q: build and image exclude each other: build makes the image", name)
	}
	build := &ResolvedBuild{Context: agent.Build.Context, Dockerfile: agent.Build.Dockerfile, Args: agent.Build.Args, Target: agent.Build.Target}
	if build.Context == "" {
		build.Context = "."
	}
	if build.Dockerfile == "" {
		build.Dockerfile = "Dockerfile"
	}
	if _, set := build.Args[HarnessImageArg]; set {
		p.addf("agent %q: build argument %s is set by egzo (the stock image of the harness)", name, HarnessImageArg)
	}
	context := BuildContext(dir, build.Context)
	if info, err := os.Stat(context); err != nil || !info.IsDir() {
		p.addf("agent %q: build context %q is not an existing directory (%s)", name, build.Context, context)
		return build
	}
	dockerfile := filepath.Clean(filepath.FromSlash(build.Dockerfile))
	if filepath.IsAbs(dockerfile) || dockerfile == ".." || strings.HasPrefix(dockerfile, ".."+string(filepath.Separator)) {
		p.addf("agent %q: build dockerfile %q is outside the build context %q: the engine reads it from the context", name, build.Dockerfile, build.Context)
	} else if info, err := os.Stat(filepath.Join(context, dockerfile)); err != nil || !info.Mode().IsRegular() {
		p.addf("agent %q: build dockerfile %q is not an existing file in the build context (%s)", name, build.Dockerfile, filepath.Join(context, dockerfile))
	}
	return build
}

// BuildContext is the directory of a build context, which the file names relative to its own directory.
func BuildContext(dir, context string) string {
	if filepath.IsAbs(context) {
		return filepath.Clean(context)
	}
	return filepath.Join(dir, context)
}

// checkSecretKey declares the project's secret key, when the file names one. That it names one is checked where the
// key is needed (MissingSecretKey): a command that only reads what `up` published, such as spawn, works without it.
func checkSecretKey(file *File, secrets map[string]SecretRef, p *problems) {
	if file.SecretKey != "" {
		if problem, _ := declareSecret(file, secrets, file.SecretKey); problem != "" {
			p.addf("secret_key: %s", problem)
		}
	}
}

// MissingSecretKey is the error of a file that names no secret key, with the command that makes one in the file's own
// vault; "" when it names one. `project check` and everything that reads the key (`up`, `restart proxy`, `rotate-ca`)
// refuse such a file.
func (r *Resolved) MissingSecretKey() string {
	if r.SecretKey != "" {
		return ""
	}
	return "secret_key is required: the secret that seals what egzo keeps at rest (the CA's private key, sign-ins). " + secretKeyHelp(r.Vaults, r.Name)
}

// secretKeyHelp says how to make a secret key in one of vaults, a pass vault if there is one.
func secretKeyHelp(vaults map[string]ResolvedVault, project string) string {
	names := slices.Sorted(maps.Keys(vaults))
	for _, name := range names {
		if vaults[name].Backend == "pass" {
			return fmt.Sprintf("Make one and name it:\n  openssl rand -hex 32 | pass insert --multiline egzo/%s-key\n  secret_key: %s/egzo/%s-key", project, name, project)
		}
	}
	for _, name := range names {
		if vaults[name].Backend == "env" {
			return fmt.Sprintf("Make one, keep it (another key cannot open what the first sealed) and name it:\n  export EGZO_PROJECT_SECRET_KEY=$(openssl rand -hex 32)\n  secret_key: %s/EGZO_PROJECT_SECRET_KEY", name)
		}
	}
	return fmt.Sprintf("Declare a vault and name a key in it:\n  vaults:\n    main: { backend: pass }\n  secret_key: main/egzo/%s-key\nthen make the key:\n  openssl rand -hex 32 | pass insert --multiline egzo/%s-key", project, project)
}

// SecretKeyCommand is the command that makes the secret key ref names, for `project init` to print.
func SecretKeyCommand(backend, name string) string {
	if backend == "env" {
		return fmt.Sprintf("export %s=$(openssl rand -hex 32)", name)
	}
	return fmt.Sprintf("openssl rand -hex 32 | pass insert --multiline %s", name)
}

// resolveUpstream checks proxy.upstream: the CA is a file of certificates and nothing else (its path made absolute, its
// content read again at `up`), a private host one exact host name.
func resolveUpstream(upstream *Upstream, dir string, p *problems) *Upstream {
	if upstream == nil {
		return nil
	}
	resolved := &Upstream{}
	if upstream.CA != "" {
		path := upstream.CA
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		resolved.CA = path
		if _, err := ReadUpstreamCA(path); err != nil {
			p.addf("proxy.upstream.ca: %v", err)
		}
	}
	for _, host := range upstream.PrivateHosts {
		normalized := names.NormalizeHost(host)
		if !names.Host(normalized) || net.ParseIP(normalized) != nil {
			p.addf("proxy.upstream.private_hosts: %q is not one exact host name (no wildcard, address, port or scheme)", host)
			continue
		}
		resolved.PrivateHosts = append(resolved.PrivateHosts, normalized)
	}
	return resolved
}

// resolveHub checks the hub's address. Plain http is allowed, as the hub allows it for a trial, with a warning: nothing then says the
// other side is the hub, and what it asks runs on the project.
func resolveHub(hub *Hub, dir string, p *problems) (*ResolvedHub, []string) {
	if hub == nil {
		return nil, nil
	}
	parsed, err := url.Parse(hub.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || strings.Trim(parsed.Path, "/") != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		p.addf("hub: %q is not an address like https://hub.example.org", hub.URL)
		return nil, nil
	}
	resolved := &ResolvedHub{URL: parsed.Scheme + "://" + parsed.Host}
	if hub.CA != "" {
		resolved.CA = hub.CA
		if !filepath.IsAbs(resolved.CA) {
			resolved.CA = filepath.Join(dir, resolved.CA)
		}
		if _, err := ReadUpstreamCA(resolved.CA); err != nil {
			p.addf("hub.ca: %v", err)
		}
	}
	if parsed.Scheme == "http" {
		return resolved, []string{fmt.Sprintf("warning: hub: %s is plain http: anyone on the way can pose as the hub, and what the hub asks runs on this project", resolved.URL)}
	}
	return resolved, nil
}

// ReadUpstreamCA reads a file of PEM certificates for the proxy to trust upstream. A private key in it, or anything that
// is not a certificate, is an error: the file goes to the proxy whole.
func ReadUpstreamCA(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", path, err)
	}
	rest, count := data, 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return nil, fmt.Errorf("%s holds a private key: it must hold certificates only", path)
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s holds a %s block: it must hold certificates only", path, block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("%s holds a certificate that cannot be read: %w", path, err)
		}
		count++
	}
	if count == 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("%s is not a file of PEM certificates", path)
	}
	return data, nil
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
