// Package stack reconciles a resolved project with what runs on the engine: it computes the
// desired resources, compares them with the labelled resources that exist, and applies the
// difference. It keeps no state: the engine is the source of truth.
package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/go-units"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/version"
)

// revision is bumped when how egzo creates resources changes, so existing projects converge.
const revision = 1

const (
	controlService = "control"
	proxyService   = "proxy"
	controlState   = "/state"
	caPrivateDir   = "/ca-private"
	caPublicDir    = "/ca-pub"
	caAgentDir     = "/etc/egzo/ca"
	proxyPort      = "3128"

	kindControl   = "control"
	kindProxy     = "proxy"
	kindAgent     = "agent"
	kindWorkspace = "workspace"
)

type NetworkSpec struct {
	Name     string
	Internal bool
	Identity engine.Identity `json:"-"`
}

type VolumeSpec struct {
	Name     string
	Identity engine.Identity `json:"-"`
}

// MountSpec is a volume (Source is the volume name) or a host directory (Source is the path).
type MountSpec struct {
	Bind     bool
	Source   string
	Target   string
	ReadOnly bool
}

type ContainerSpec struct {
	Name        string
	Image       string
	Cmd         []string
	Env         []string
	Mounts      []MountSpec
	Tmpfs       map[string]string
	Network     string
	WorkingDir  string
	Harness     string
	Runtime     string
	NanoCPUs    int64
	Memory      int64
	Healthcheck []string
	// Agents run under an init process that reaps children and forwards signals.
	Init bool
	// Hardened sidecars get a read-only root filesystem. Agents need a writable one.
	ReadonlyRootfs bool
	Identity       engine.Identity `json:"-"`
}

// Attachment joins a sidecar to every agent network under an alias. It is not part of the sidecar's
// definition: agents come and go without recreating the sidecar.
type Attachment struct {
	Container string
	Alias     string
	Networks  []string
}

// Desired is everything a project should consist of.
type Desired struct {
	Networks    []NetworkSpec
	Volumes     []VolumeSpec
	Containers  []ContainerSpec
	Attachments []Attachment
	Control     string
	Proxy       string // empty when the project has no agents
}

// Inputs are what Desire needs besides the resolved project.
type Inputs struct {
	// Image is the all-in-one egzo image that runs every sidecar role.
	Image string
	// Tokens maps each agent to the token that identifies it to the proxy.
	Tokens map[string]string
}

// Desire computes the desired resources of a project.
func Desire(project *config.Resolved, dir string, in Inputs) (Desired, error) {
	identity := func(service, kind string) engine.Identity {
		return engine.Identity{Project: project.Name, Service: service, Kind: kind, ProjectDir: dir}
	}
	var desired Desired

	controlNetwork := NetworkSpec{Name: project.Name + "_control", Internal: true, Identity: identity(controlService, kindControl)}
	controlVolume := VolumeSpec{Name: project.Name + "_control", Identity: identity(controlService, kindControl)}
	control := ContainerSpec{
		Name:           project.Name + "-control-1",
		Image:          in.Image,
		Cmd:            []string{"/egzo", "control"},
		Env:            []string{"EGZO_PROJECT=" + project.Name},
		Mounts:         []MountSpec{{Source: controlVolume.Name, Target: controlState}},
		Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,size=16m"},
		Network:        controlNetwork.Name,
		Healthcheck:    []string{"/egzo", "control", "--healthcheck"},
		ReadonlyRootfs: true,
		Identity:       identity(controlService, kindControl),
	}
	desired.Control = control.Name

	// Declared workspaces without a source are shared volumes.
	workspaceNames := sortedKeys(project.Workspaces)
	volumes := []VolumeSpec{controlVolume}
	for _, name := range workspaceNames {
		if project.Workspaces[name].Git == nil {
			volumes = append(volumes, VolumeSpec{Name: project.Name + "_" + name, Identity: identity(name, kindWorkspace)})
		}
	}

	networks := []NetworkSpec{controlNetwork}
	containers := []ContainerSpec{control}
	agentNames := sortedKeys(project.Agents)

	var agentNetworks []string
	for _, name := range agentNames {
		agentNetworks = append(agentNetworks, project.Name+"_"+name)
	}
	desired.Attachments = []Attachment{{Container: control.Name, Alias: "control", Networks: agentNetworks}}

	if len(agentNames) > 0 {
		egress := NetworkSpec{Name: project.Name + "_egress", Identity: identity(proxyService, kindProxy)}
		caPrivate := VolumeSpec{Name: project.Name + "_ca-private", Identity: identity(proxyService, kindProxy)}
		caPublic := VolumeSpec{Name: project.Name + "_ca", Identity: identity(proxyService, kindProxy)}
		proxy := ContainerSpec{
			Name:  project.Name + "-proxy-1",
			Image: in.Image,
			Cmd:   []string{"/egzo", "proxy", "serve"},
			Mounts: []MountSpec{
				{Source: caPrivate.Name, Target: caPrivateDir},
				{Source: caPublic.Name, Target: caPublicDir},
			},
			Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,size=16m", "/run/egzo": "rw,nosuid,size=1m"},
			Network:        egress.Name,
			Healthcheck:    []string{"/egzo", "proxy", "healthcheck"},
			ReadonlyRootfs: true,
			Identity:       identity(proxyService, kindProxy),
		}
		networks = append(networks, egress)
		volumes = append(volumes, caPrivate, caPublic)
		containers = append(containers, proxy)
		desired.Proxy = proxy.Name
		desired.Attachments = append(desired.Attachments, Attachment{Container: proxy.Name, Alias: "proxy", Networks: agentNetworks})
	}

	for _, name := range agentNames {
		network := NetworkSpec{Name: project.Name + "_" + name, Internal: true, Identity: identity(name, kindAgent)}
		spec, err := agentContainer(project, name, project.Agents[name], network.Name, in.Tokens[name], identity(name, kindAgent))
		if err != nil {
			return desired, err
		}
		networks = append(networks, network)
		containers = append(containers, spec)
	}

	for i := range networks {
		networks[i].Identity.ConfigHash = hash(networks[i])
	}
	for i := range volumes {
		volumes[i].Identity.ConfigHash = hash(volumes[i])
	}
	for i := range containers {
		containers[i].Identity.ConfigHash = hash(containers[i])
	}
	desired.Networks, desired.Volumes, desired.Containers = networks, volumes, containers
	return desired, nil
}

func agentContainer(
	project *config.Resolved, name string, agent config.ResolvedAgent, network, token string, identity engine.Identity,
) (ContainerSpec, error) {
	spec := ContainerSpec{
		Name:       project.Name + "-" + name + "-1",
		Image:      agentImage(agent),
		Network:    network,
		WorkingDir: agent.Workdir,
		Harness:    agent.Harness,
		Init:       true,
		Identity:   identity,
	}

	// Agents reach the world only through the proxy and trust only the project CA on top of the
	// usual roots. The token identifies the agent to the proxy; it is not a provider secret.
	proxyURL := fmt.Sprintf("http://%s:%s@proxy:%s", name, token, proxyPort)
	bundle, certificate := caAgentDir+"/ca-bundle.crt", caAgentDir+"/ca.crt"
	env := map[string]string{
		"EGZO_PROJECT":        project.Name,
		"EGZO_AGENT":          name,
		"HTTPS_PROXY":         proxyURL,
		"https_proxy":         proxyURL,
		"HTTP_PROXY":          proxyURL,
		"http_proxy":          proxyURL,
		"NO_PROXY":            "control,localhost,127.0.0.1",
		"no_proxy":            "control,localhost,127.0.0.1",
		"SSL_CERT_FILE":       bundle,
		"REQUESTS_CA_BUNDLE":  bundle,
		"CURL_CA_BUNDLE":      bundle,
		"GIT_SSL_CAINFO":      bundle,
		"NODE_EXTRA_CA_CERTS": certificate,
	}
	for key, value := range agent.Env {
		env[key] = value
	}
	for _, key := range sortedKeys(env) {
		spec.Env = append(spec.Env, key+"="+env[key])
	}
	spec.Mounts = append(spec.Mounts, MountSpec{Source: project.Name + "_ca", Target: caAgentDir, ReadOnly: true})

	var workspaceNames []string
	for _, mount := range agent.Workspaces {
		switch {
		case mount.HostPath != "":
			spec.Mounts = append(spec.Mounts, MountSpec{Bind: true, Source: mount.HostPath, Target: mount.Mount, ReadOnly: mount.Mode == "ro"})
		default:
			declared, ok := project.Workspaces[mount.Name]
			if !ok || declared.Git != nil {
				return spec, fmt.Errorf("agent %q: workspace %q is a git workspace; cloning git workspaces is not implemented yet", name, mount.Name)
			}
			spec.Mounts = append(spec.Mounts, MountSpec{Source: project.Name + "_" + mount.Name, Target: mount.Mount, ReadOnly: mount.Mode == "ro"})
		}
		workspaceNames = append(workspaceNames, strings.TrimPrefix(mount.Mount, "/workspace/"))
	}

	isolation := agent.Isolation
	if isolation == "" {
		isolation = project.Runtime.Isolation
	}
	if isolation == "gvisor" {
		spec.Runtime = "runsc"
	}
	if agent.Resources.CPUs > 0 {
		spec.NanoCPUs = int64(agent.Resources.CPUs * 1e9)
	}
	if agent.Resources.Memory != "" {
		memory, err := units.RAMInBytes(agent.Resources.Memory)
		if err != nil {
			return spec, fmt.Errorf("agent %q: invalid memory %q: %w", name, agent.Resources.Memory, err)
		}
		spec.Memory = memory
	}

	spec.Identity.Extra = map[string]string{
		engine.LabelAgentHarness:    agent.Harness,
		engine.LabelAgentWorkspaces: strings.Join(workspaceNames, ","),
	}
	return spec, nil
}

func agentImage(agent config.ResolvedAgent) string {
	if agent.Image != "" {
		return agent.Image
	}
	return "ghcr.io/egzo-ai/egzo-harness-" + agent.Harness + ":" + version.Version
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// hash fingerprints a resource definition. Identity is excluded from the hashed JSON, so the
// project directory, which does not change what runs, never forces a recreate; the semantic
// labels are hashed through the spec fields they derive from.
func hash(spec any) string {
	data, err := json.Marshal(struct {
		Revision int
		Spec     any
	}{revision, spec})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
