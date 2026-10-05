// Package stack reconciles a resolved project with what runs on the engine: it computes the
// desired resources, compares them with the labelled resources that exist, and applies the
// difference. It keeps no state: the engine is the source of truth.
package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/go-units"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/harness"
	"github.com/egzo-ai/egzo/internal/version"
)

// revision is bumped when how egzo creates resources changes, so existing projects converge.
const revision = 1

const (
	workspaceRoot  = "/workspace"
	controlService = "control"
	proxyService   = "proxy"
	controlState   = "/state"
	caPrivateDir   = "/ca-private"
	caPublicDir    = "/ca-pub"
	caAgentDir     = "/etc/egzo/ca"
	promptPath     = "/etc/egzo/prompt.md"
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
	Name  string
	Image string
	Cmd   []string
	User  string
	// OwnedVolumes are volumes the container writes as User: the engine creates volumes owned by
	// root and agents do not run as root, so they are handed over once the container exists.
	OwnedVolumes []string
	// StartAfter names the containers this one is created after (depends_on). It does not change
	// what the container is, so it is left out of the hash.
	StartAfter  []string `json:"-"`
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
	// PrepImage runs the short-lived prep containers (git, volume ownership).
	PrepImage string
}

// Inputs are what Desire needs besides the resolved project.
type Inputs struct {
	// Image is the all-in-one egzo image that runs every sidecar role.
	Image string
	// Tokens maps each agent to the token that identifies it to the proxy.
	Tokens map[string]string
	// User is the "uid:gid" agents run as, so what they write on the host is the invoking user's.
	// Empty leaves it to the image (Podman maps users itself).
	User string
	// HarnessPrefix is the start of a harness image's name, ending in "egzo-harness-".
	HarnessPrefix string
}

// DefaultHarnessPrefix is where harness images are published.
const DefaultHarnessPrefix = "ghcr.io/egzo-ai/egzo-harness-"

// Desire computes the desired resources of a project.
func Desire(project *config.Resolved, dir string, in Inputs) (Desired, error) {
	identity := func(service, kind string) engine.Identity {
		return engine.Identity{Project: project.Name, Service: service, Kind: kind, ProjectDir: dir}
	}
	var desired Desired
	desired.PrepImage = in.Image

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
		proxyImage := in.Image
		if project.Proxy != nil && project.Proxy.Image != "" {
			proxyImage = project.Proxy.Image
		}
		proxy := ContainerSpec{
			Name:  project.Name + "-proxy-1",
			Image: proxyImage,
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
		spec, err := agentContainer(project, dir, name, project.Agents[name], network.Name, in, identity(name, kindAgent))
		if err != nil {
			return desired, err
		}
		for _, dependency := range project.Agents[name].DependsOn {
			spec.StartAfter = append(spec.StartAfter, project.Name+"-"+dependency+"-1")
		}
		networks = append(networks, network)
		containers = append(containers, spec)
		if hasHome(project.Agents[name]) {
			volumes = append(volumes, VolumeSpec{Name: homeVolume(project.Name, name), Identity: identity(name, kindAgent)})
		}
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

// homeDir is where a harness image keeps its home. It is a volume, so the harness's own state, such
// as conversations to resume, outlives recreating the agent.
const homeDir = "/home/agent"

func hasHome(agent config.ResolvedAgent) bool { return agent.Harness != "custom" }

func homeVolume(project, agent string) string { return project + "_" + agent + "-home" }

func agentContainer(
	project *config.Resolved, dir, name string, agent config.ResolvedAgent, network string, in Inputs, identity engine.Identity,
) (ContainerSpec, error) {
	token := in.Tokens[name]
	spec := ContainerSpec{
		Name:       project.Name + "-" + name + "-1",
		Image:      agentImage(agent, in.HarnessPrefix),
		User:       in.User,
		Network:    network,
		WorkingDir: agent.Workdir,
		Harness:    agent.Harness,
		Init:       true,
		Identity:   identity,
	}

	// Agents reach the world only through the proxy and trust only the project CA on top of the
	// usual roots. The token identifies the agent to the proxy; it is not a provider secret.
	env := proxyEnv(project.Name, name, token)
	env["EGZO_PROJECT"] = project.Name
	env["EGZO_AGENT"] = name
	env["EGZO_CONTROL_URL"] = "http://control:7777"
	env["EGZO_TOKEN"] = token
	env["EGZO_HARNESS"] = agent.Harness
	env["EGZO_PERMISSIONS"] = agent.Permissions
	env["EGZO_HUMAN_QUIET"] = agent.Inject.HumanQuiet
	env["EGZO_ACK_TIMEOUT"] = agent.Inject.AckTimeout
	env["EGZO_IDLE_SIGNAL"] = agent.Inject.IdleSignal
	env["EGZO_QUIESCENCE"] = agent.Inject.Quiescence
	if agent.Model != "" {
		env["EGZO_MODEL"] = agent.Model
	}
	if integration, ok := harness.For(agent.Harness); ok {
		for key, value := range integration.ContainerEnv(agent.Permissions != "default", bearerProvider(project, agent)) {
			env[key] = value
		}
	}
	for key, value := range agent.Env {
		env[key] = value
	}
	if agent.Prompt != "" {
		prompt := agent.Prompt
		if !filepath.IsAbs(prompt) {
			prompt = filepath.Join(dir, prompt)
		}
		spec.Mounts = append(spec.Mounts, MountSpec{Bind: true, Source: filepath.Clean(prompt), Target: promptPath, ReadOnly: true})
		env["EGZO_PROMPT_FILE"] = promptPath
	}
	if hasHome(agent) {
		spec.Mounts = append(spec.Mounts, MountSpec{Source: homeVolume(project.Name, name), Target: homeDir})
		if in.User != "" {
			spec.OwnedVolumes = append(spec.OwnedVolumes, homeVolume(project.Name, name))
		}
		// An engine gives a user with no passwd entry the home "/", which nobody can write.
		if _, set := agent.Env["HOME"]; !set {
			env["HOME"] = homeDir
		}
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
			if !ok {
				return spec, fmt.Errorf("agent %q: workspace %q is not declared", name, mount.Name)
			}
			if declared.Git != nil {
				owner := name
				if mount.From != "" {
					owner = mount.From
				}
				spec.Mounts = append(spec.Mounts, MountSpec{Bind: true, Source: CheckoutDir(declared, owner), Target: mount.Mount, ReadOnly: mount.Mode == "ro"})
				if declared.Mode == "worktree" && mount.From == "" {
					spec.Mounts = append(spec.Mounts, MountSpec{Bind: true, Source: BaseDir(declared), Target: baseMountPath(mount.Name)})
				}
				workspaceNames = append(workspaceNames, strings.TrimPrefix(mount.Mount, "/workspace/"))
				continue
			}
			spec.Mounts = append(spec.Mounts, MountSpec{Source: project.Name + "_" + mount.Name, Target: mount.Mount, ReadOnly: mount.Mode == "ro"})
			if mount.Mode != "ro" && in.User != "" {
				spec.OwnedVolumes = append(spec.OwnedVolumes, project.Name+"_"+mount.Name)
			}
		}
		workspaceNames = append(workspaceNames, strings.TrimPrefix(mount.Mount, "/workspace/"))
	}

	spec.Runtime = agent.Runtime
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

	if len(workspaceNames) > 0 {
		var paths []string
		for _, mount := range agent.Workspaces {
			paths = append(paths, mount.Mount)
		}
		env2 := "EGZO_WORKSPACES=" + strings.Join(paths, ":")
		spec.Env = append(spec.Env, env2)
		sort.Strings(spec.Env)
	}

	spec.Identity.Extra = map[string]string{
		engine.LabelAgentHarness:    agent.Harness,
		engine.LabelAgentWorkspaces: strings.Join(workspaceNames, ","),
	}
	return spec, nil
}

func agentImage(agent config.ResolvedAgent, prefix string) string {
	if agent.Image != "" {
		return agent.Image
	}
	if prefix == "" {
		prefix = DefaultHarnessPrefix
	}
	return prefix + agent.Harness + ":" + version.Version
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

// bearerProvider reports whether an agent's profile injects the Anthropic credential as a bearer
// token, which is how a Claude subscription token is sent.
func bearerProvider(project *config.Resolved, agent config.ResolvedAgent) bool {
	profile := project.Egress[agent.Egress]
	if profile == nil {
		return false
	}
	for _, service := range profile.Services {
		if service.Inject == nil || !strings.EqualFold(service.Inject.Header, "Authorization") {
			continue
		}
		for _, host := range service.Hosts {
			if host == "api.anthropic.com" {
				return true
			}
		}
	}
	return false
}
