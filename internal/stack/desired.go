// Package stack reconciles a resolved project with what runs on the engine: it computes the
// desired resources, compares them with the labelled resources that exist, and applies the
// difference. It keeps no state: the engine is the source of truth.
package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
	Env          []string
	Mounts       []MountSpec
	Tmpfs        map[string]string
	Network      string
	WorkingDir   string
	Harness      string
	Runtime      string
	NanoCPUs     int64
	Memory       int64
	Healthcheck  []string
	// PidsLimit caps processes and threads; MemorySwap equals Memory when a memory limit is set, so
	// swap does not double it.
	PidsLimit  int64
	MemorySwap int64
	// BoundedLogs caps the engine's log of the container, which otherwise grows without limit.
	BoundedLogs bool
	// PromptDigest is the content hash of the agent's prompt file: the file is mounted, so only this
	// makes a changed prompt a changed agent.
	PromptDigest string
	// Agents run under an init process that reaps children and forwards signals.
	Init bool
	// Hardened sidecars get a read-only root filesystem. Agents need a writable one.
	ReadonlyRootfs bool
	Identity       engine.Identity `json:"-"`
}

// Attachment joins a sidecar to agent networks under an alias. It is not part of the sidecar's
// definition: instances come and go without recreating the sidecar.
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
	// Tokens maps each instance to the token that identifies it to the proxy and control.
	Tokens map[string]string
	// User is the "uid:gid" agents run as, so what they write on the host is the invoking user's.
	// Empty leaves it to the image (Podman maps users itself).
	User string
	// HarnessPrefix is the start of a harness image's name, ending in "egzo-harness-".
	HarnessPrefix string
	// PromptDigests maps each agent that has a prompt file to the hash of its content (see PromptDigests).
	PromptDigests map[string]string
	// Instances are the names of the instances that exist: the sidecars are attached to their networks.
	Instances []string
}

// The sidecars run as an unprivileged user and are limited in memory and processes: they parse what
// agents send them, and a compromise or a runaway must stay small. Agents get a generous process limit,
// because a fork bomb in one must not take the host.
const (
	sidecarUser       = "65532:65532"
	sidecarTmpfs      = "rw,noexec,nosuid,size=16m,uid=65532,gid=65532"
	controlMemory     = 512 << 20
	controlPids       = 512
	proxyMemory       = 1 << 30
	proxyPids         = 2048
	defaultAgentPids  = 4096
	sidecarTmpfsState = "rw,nosuid,size=1m,uid=65532,gid=65532"
)

// PromptDigests hashes the prompt file of every agent that has one, so that editing a prompt changes the
// agent's definition. A prompt that cannot be read is an error.
func PromptDigests(project *config.Resolved, dir string) (map[string]string, error) {
	digests := map[string]string{}
	for name, agent := range project.Agents {
		if agent.Prompt == "" {
			continue
		}
		path := agent.Prompt
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("agent %q: cannot read its prompt: %w", name, err)
		}
		sum := sha256.Sum256(data)
		digests[name] = hex.EncodeToString(sum[:])
	}
	return digests, nil
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
		User:           sidecarUser,
		OwnedVolumes:   []string{controlVolume.Name},
		Memory:         controlMemory,
		MemorySwap:     controlMemory,
		PidsLimit:      controlPids,
		BoundedLogs:    true,
		Env:            []string{"EGZO_PROJECT=" + project.Name},
		Mounts:         []MountSpec{{Source: controlVolume.Name, Target: controlState}},
		Tmpfs:          map[string]string{"/tmp": sidecarTmpfs},
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

	// The sidecars join the network of every instance. The instances are spawned, not declared: the
	// attachments only keep the ones that exist when a sidecar is created again.
	var instanceNetworks []string
	for _, name := range in.Instances {
		instanceNetworks = append(instanceNetworks, project.Name+"_"+name)
	}
	desired.Attachments = []Attachment{{Container: control.Name, Alias: "control", Networks: instanceNetworks}}

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
			User:           sidecarUser,
			OwnedVolumes:   []string{caPrivate.Name, caPublic.Name},
			Memory:         proxyMemory,
			MemorySwap:     proxyMemory,
			PidsLimit:      proxyPids,
			BoundedLogs:    true,
			Tmpfs:          map[string]string{"/tmp": sidecarTmpfs, "/run/egzo": sidecarTmpfsState},
			Network:        egress.Name,
			Healthcheck:    []string{"/egzo", "proxy", "healthcheck"},
			ReadonlyRootfs: true,
			Identity:       identity(proxyService, kindProxy),
		}
		networks = append(networks, egress)
		volumes = append(volumes, caPrivate, caPublic)
		containers = append(containers, proxy)
		desired.Proxy = proxy.Name
		desired.Attachments = append(desired.Attachments, Attachment{Container: proxy.Name, Alias: "proxy", Networks: instanceNetworks})
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

// InstanceContainer is the name of an instance's container.
func InstanceContainer(project, instance string) string { return project + "-" + instance }

// InstanceNetwork is the name of an instance's network.
func InstanceNetwork(project, instance string) string { return project + "_" + instance }

// InstanceIdentity says where an instance came from.
type InstanceIdentity struct {
	Template     string
	Actor        string
	TemplateHash string
}

// DesireInstance computes the resources of one instance: its network, its home volume, its container,
// and the attachments of the sidecars to its network. view is the project with the instance as an
// agent (see Published.View); name is the instance's key in it.
func DesireInstance(view *config.Resolved, dir string, in Inputs, name string, from InstanceIdentity) (Desired, error) {
	identity := func(kind string) engine.Identity {
		return engine.Identity{
			Project: view.Name, Service: from.Template, Kind: kind, ProjectDir: dir,
			Instance: name, Actor: from.Actor, TemplateHash: from.TemplateHash,
		}
	}
	agent, ok := view.Agents[name]
	if !ok {
		return Desired{}, fmt.Errorf("no instance %q in the project view", name)
	}
	desired := Desired{PrepImage: in.Image, Control: view.Name + "-control-1"}
	network := NetworkSpec{Name: InstanceNetwork(view.Name, name), Internal: true, Identity: identity(kindAgent)}
	spec, err := agentContainer(view, dir, name, agent, network.Name, in, identity(kindAgent))
	if err != nil {
		return desired, err
	}
	desired.Networks = []NetworkSpec{network}
	desired.Containers = []ContainerSpec{spec}
	if hasHome(agent) {
		desired.Volumes = []VolumeSpec{{Name: homeVolume(view.Name, name), Identity: identity(kindAgent)}}
	}
	desired.Attachments = []Attachment{{Container: desired.Control, Alias: "control", Networks: []string{network.Name}}}
	if len(view.Agents) > 0 {
		desired.Proxy = view.Name + "-proxy-1"
		desired.Attachments = append(desired.Attachments, Attachment{Container: desired.Proxy, Alias: "proxy", Networks: []string{network.Name}})
	}
	for i := range desired.Networks {
		desired.Networks[i].Identity.ConfigHash = hash(desired.Networks[i])
	}
	for i := range desired.Volumes {
		desired.Volumes[i].Identity.ConfigHash = hash(desired.Volumes[i])
	}
	for i := range desired.Containers {
		desired.Containers[i].Identity.ConfigHash = hash(desired.Containers[i])
	}
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
		Name:       InstanceContainer(project.Name, name),
		Image:      agentImage(agent, in.HarnessPrefix),
		User:       in.User,
		Network:    network,
		WorkingDir: agent.Workdir,
		Harness:    agent.Harness,
		Init:       true,
		Identity:   identity,

		PidsLimit:    defaultAgentPids,
		BoundedLogs:  true,
		PromptDigest: in.PromptDigests[name],
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
				spec.Mounts = append(spec.Mounts, MountSpec{Bind: true, Source: CheckoutDir(declared, name), Target: mount.Mount, ReadOnly: mount.Mode == "ro"})
				if declared.Mode == "worktree" {
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
		spec.MemorySwap = memory
	}
	if agent.Resources.Pids > 0 {
		spec.PidsLimit = agent.Resources.Pids
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
