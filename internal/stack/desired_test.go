package stack

import (
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

func desireProject() *config.Resolved {
	return &config.Resolved{
		Name:       "proj",
		Workspaces: map[string]config.ResolvedWorkspace{"shared": {}},
		Agents: map[string]config.ResolvedAgent{
			"coder": {
				Harness:    "claude-code",
				Workdir:    "/workspace/shared",
				Egress:     "default",
				Workspaces: []config.Mount{{Name: "shared", Mount: "/workspace/shared", Mode: "rw"}},
				Env:        map[string]string{"LOG": "debug"},
			},
			"review": {
				Harness:    "custom",
				Image:      "example/review:1",
				Workdir:    "/workspace",
				Egress:     "default",
				Workspaces: []config.Mount{{Name: "src", Mount: "/workspace/src", Mode: "ro", HostPath: "/host/src"}},
			},
		},
	}
}

var desireInputs = Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "tok-coder", "review": "tok-review"}}

func desire(t *testing.T, project *config.Resolved) Desired {
	t.Helper()
	desired, err := Desire(project, "/dir", desireInputs)
	if err != nil {
		t.Fatalf("Desire: %v", err)
	}
	return desired
}

func findContainer(t *testing.T, d Desired, name string) ContainerSpec {
	t.Helper()
	for _, c := range d.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %q in %v", name, containerNames(d))
	return ContainerSpec{}
}

func containerNames(d Desired) []string {
	var names []string
	for _, c := range d.Containers {
		names = append(names, c.Name)
	}
	return names
}

func TestDesireLayout(t *testing.T) {
	d := desire(t, desireProject())

	if want := []string{"proj-control-1", "proj-proxy-1", "proj-coder-1", "proj-review-1"}; !slices.Equal(containerNames(d), want) {
		t.Errorf("containers = %v, want %v", containerNames(d), want)
	}
	if d.Control != "proj-control-1" || d.Proxy != "proj-proxy-1" {
		t.Errorf("control %q proxy %q", d.Control, d.Proxy)
	}

	networks := map[string]bool{}
	for _, n := range d.Networks {
		networks[n.Name] = n.Internal
	}
	wantNetworks := map[string]bool{
		"proj_control": true, "proj_egress": false, "proj_coder": true, "proj_review": true,
	}
	if len(networks) != len(wantNetworks) {
		t.Errorf("networks = %v, want %v", networks, wantNetworks)
	}
	for name, internal := range wantNetworks {
		if got, ok := networks[name]; !ok || got != internal {
			t.Errorf("network %s: present %v internal %v, want internal %v", name, ok, got, internal)
		}
	}

	var volumes []string
	for _, v := range d.Volumes {
		volumes = append(volumes, v.Name)
	}
	if want := []string{"proj_control", "proj_shared", "proj_ca-private", "proj_ca", "proj_coder-home"}; !slices.Equal(volumes, want) {
		t.Errorf("volumes = %v, want %v", volumes, want)
	}
}

func TestDesireAttachesSidecarsToEveryAgentNetwork(t *testing.T) {
	d := desire(t, desireProject())
	want := []Attachment{
		{Container: "proj-control-1", Alias: "control", Networks: []string{"proj_coder", "proj_review"}},
		{Container: "proj-proxy-1", Alias: "proxy", Networks: []string{"proj_coder", "proj_review"}},
	}
	if len(d.Attachments) != len(want) {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
	for i, a := range want {
		got := d.Attachments[i]
		if got.Container != a.Container || got.Alias != a.Alias || !slices.Equal(got.Networks, a.Networks) {
			t.Errorf("attachment %d = %+v, want %+v", i, got, a)
		}
	}
}

func TestDesireWithoutAgentsHasNoProxy(t *testing.T) {
	d := desire(t, &config.Resolved{Name: "proj"})
	if d.Proxy != "" {
		t.Errorf("proxy = %q, want none", d.Proxy)
	}
	if want := []string{"proj-control-1"}; !slices.Equal(containerNames(d), want) {
		t.Errorf("containers = %v", containerNames(d))
	}
	if len(d.Attachments) != 1 || len(d.Attachments[0].Networks) != 0 {
		t.Errorf("attachments = %+v", d.Attachments)
	}
}

func TestDesireSidecarsAreHardened(t *testing.T) {
	d := desire(t, desireProject())
	for _, name := range []string{"proj-control-1", "proj-proxy-1"} {
		c := findContainer(t, d, name)
		if !c.ReadonlyRootfs {
			t.Errorf("%s has a writable root filesystem", name)
		}
		if c.Image != "egzo:test" {
			t.Errorf("%s image = %q", name, c.Image)
		}
		if len(c.Healthcheck) == 0 {
			t.Errorf("%s has no healthcheck", name)
		}
		if c.Init {
			t.Errorf("%s runs under init; only agents do", name)
		}
	}
	if c := findContainer(t, d, "proj-coder-1"); c.ReadonlyRootfs || !c.Init {
		t.Errorf("agent: readonly=%v init=%v, want a writable rootfs under init", c.ReadonlyRootfs, c.Init)
	}
}

func TestDesireAgentContainer(t *testing.T) {
	d := desire(t, desireProject())
	coder := findContainer(t, d, "proj-coder-1")

	if coder.Network != "proj_coder" || coder.WorkingDir != "/workspace/shared" || coder.Harness != "claude-code" {
		t.Errorf("coder = %+v", coder)
	}
	if want := "ghcr.io/egzo-ai/egzo-harness-claude-code:"; !strings.HasPrefix(coder.Image, want) {
		t.Errorf("default image = %q, want prefix %q", coder.Image, want)
	}
	if findContainer(t, d, "proj-review-1").Image != "example/review:1" {
		t.Error("an explicit image was not used")
	}

	if !slices.IsSorted(coder.Env) {
		t.Errorf("env is not sorted, so the config hash would be unstable: %v", coder.Env)
	}
	for _, want := range []string{
		"EGZO_AGENT=coder", "EGZO_PROJECT=proj", "EGZO_TOKEN=tok-coder", "LOG=debug",
		"HTTPS_PROXY=http://coder:tok-coder@proxy:3128",
		"SSL_CERT_FILE=/etc/egzo/ca/ca-bundle.crt", "NODE_EXTRA_CA_CERTS=/etc/egzo/ca/ca.crt",
		"EGZO_CONTROL_URL=http://control:7777",
	} {
		if !slices.Contains(coder.Env, want) {
			t.Errorf("env lacks %s\n%v", want, coder.Env)
		}
	}

	wantMounts := []MountSpec{
		{Source: "proj_coder-home", Target: "/home/agent"},
		{Source: "proj_ca", Target: "/etc/egzo/ca", ReadOnly: true},
		{Source: "proj_shared", Target: "/workspace/shared"},
	}
	if !slices.Equal(coder.Mounts, wantMounts) {
		t.Errorf("mounts = %+v, want %+v", coder.Mounts, wantMounts)
	}
	review := findContainer(t, d, "proj-review-1")
	if want := (MountSpec{Bind: true, Source: "/host/src", Target: "/workspace/src", ReadOnly: true}); !slices.Contains(review.Mounts, want) {
		t.Errorf("review mounts = %+v, want %+v", review.Mounts, want)
	}
}

func TestDesireAgentEnvOverridesTheDefaults(t *testing.T) {
	project := desireProject()
	agent := project.Agents["coder"]
	agent.Env = map[string]string{"NO_PROXY": "custom"}
	project.Agents["coder"] = agent
	coder := findContainer(t, desire(t, project), "proj-coder-1")
	if !slices.Contains(coder.Env, "NO_PROXY=custom") || slices.Contains(coder.Env, "NO_PROXY=control,localhost,127.0.0.1") {
		t.Errorf("env = %v", coder.Env)
	}
}

func TestDesireResources(t *testing.T) {
	project := desireProject()
	agent := project.Agents["coder"]
	agent.Resources = config.Resources{CPUs: 1.5, Memory: "2g"}
	agent.Runtime = "runsc"
	project.Agents["coder"] = agent
	coder := findContainer(t, desire(t, project), "proj-coder-1")

	if coder.NanoCPUs != 1_500_000_000 {
		t.Errorf("NanoCPUs = %d", coder.NanoCPUs)
	}
	if coder.Memory != 2<<30 {
		t.Errorf("Memory = %d", coder.Memory)
	}
	if coder.Runtime != "runsc" {
		t.Errorf("Runtime = %q", coder.Runtime)
	}
	if review := findContainer(t, desire(t, project), "proj-review-1"); review.NanoCPUs != 0 || review.Memory != 0 {
		t.Errorf("unset resources should stay zero: %+v", review)
	}
}

func TestDesireErrors(t *testing.T) {
	t.Run("invalid memory", func(t *testing.T) {
		project := desireProject()
		agent := project.Agents["coder"]
		agent.Resources.Memory = "lots"
		project.Agents["coder"] = agent
		if _, err := Desire(project, "/dir", desireInputs); err == nil || !strings.Contains(err.Error(), "invalid memory") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an undeclared workspace", func(t *testing.T) {
		project := desireProject()
		agent := project.Agents["coder"]
		agent.Workspaces = []config.Mount{{Name: "ghost", Mount: "/workspace/ghost", Mode: "rw"}}
		project.Agents["coder"] = agent
		if _, err := Desire(project, "/dir", desireInputs); err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDesireIdentityAndLabels(t *testing.T) {
	d := desire(t, desireProject())
	coder := findContainer(t, d, "proj-coder-1")
	labels := coder.Identity.Labels()

	for key, want := range map[string]string{
		engine.LabelProject:         "proj",
		engine.LabelService:         "coder",
		engine.LabelKind:            "agent",
		engine.LabelProjectDir:      "/dir",
		engine.LabelAgentHarness:    "claude-code",
		engine.LabelAgentWorkspaces: "shared",
		engine.LabelConfigHash:      coder.Identity.ConfigHash,
	} {
		if labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if coder.Identity.ConfigHash == "" {
		t.Error("no config hash")
	}
	if kind := findContainer(t, d, "proj-proxy-1").Identity.Kind; kind != "proxy" {
		t.Errorf("proxy kind = %q", kind)
	}
	if kind := findContainer(t, d, "proj-control-1").Identity.Kind; kind != "control" {
		t.Errorf("control kind = %q", kind)
	}
	for _, v := range d.Volumes {
		if v.Name == "proj_shared" && v.Identity.Kind != "workspace" {
			t.Errorf("workspace volume kind = %q", v.Identity.Kind)
		}
		if v.Identity.ConfigHash == "" {
			t.Errorf("volume %s has no config hash", v.Name)
		}
	}
}

func TestDesireIsDeterministic(t *testing.T) {
	a, b := desire(t, desireProject()), desire(t, desireProject())
	for i := range a.Containers {
		if a.Containers[i].Identity.ConfigHash != b.Containers[i].Identity.ConfigHash {
			t.Errorf("%s hash changed between identical runs", a.Containers[i].Name)
		}
	}
}

func TestConfigHashTracksWhatRunsNotWhereItIsDeclared(t *testing.T) {
	base := findContainer(t, desire(t, desireProject()), "proj-coder-1").Identity.ConfigHash

	moved, err := Desire(desireProject(), "/another/dir", desireInputs)
	if err != nil {
		t.Fatal(err)
	}
	if got := findContainer(t, moved, "proj-coder-1").Identity.ConfigHash; got != base {
		t.Error("moving the project directory changed the config hash, which would recreate every container")
	}

	for name, change := range map[string]func(*config.Resolved, *Inputs){
		"image": func(p *config.Resolved, _ *Inputs) {
			a := p.Agents["coder"]
			a.Image = "other:1"
			p.Agents["coder"] = a
		},
		"env": func(p *config.Resolved, _ *Inputs) {
			a := p.Agents["coder"]
			a.Env = map[string]string{"X": "1"}
			p.Agents["coder"] = a
		},
		"token": func(_ *config.Resolved, in *Inputs) { in.Tokens = map[string]string{"coder": "different"} },
		"memory": func(p *config.Resolved, _ *Inputs) {
			a := p.Agents["coder"]
			a.Resources.Memory = "1g"
			p.Agents["coder"] = a
		},
	} {
		project, inputs := desireProject(), Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "tok-coder"}}
		change(project, &inputs)
		changed, err := Desire(project, "/dir", inputs)
		if err != nil {
			t.Fatal(err)
		}
		if got := findContainer(t, changed, "proj-coder-1").Identity.ConfigHash; got == base {
			t.Errorf("changing the %s did not change the config hash", name)
		}
	}
}

func TestAgentImage(t *testing.T) {
	if got := agentImage(config.ResolvedAgent{Image: "mine:1", Harness: "pi"}, ""); got != "mine:1" {
		t.Errorf("explicit image = %q", got)
	}
	if got := agentImage(config.ResolvedAgent{Harness: "pi"}, ""); !strings.HasPrefix(got, "ghcr.io/egzo-ai/egzo-harness-pi:") {
		t.Errorf("default image = %q", got)
	}
	if got := agentImage(config.ResolvedAgent{Harness: "pi"}, "localhost:5000/h-"); !strings.HasPrefix(got, "localhost:5000/h-pi:") {
		t.Errorf("image with a registry prefix = %q", got)
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]int{"b": 1, "c": 2, "a": 3})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("sortedKeys = %v", got)
	}
	if got := sortedKeys[int](nil); len(got) != 0 {
		t.Errorf("sortedKeys(nil) = %v", got)
	}
}

func TestAgentsRunAsTheGivenUserAndNewWorkspaceVolumesAreHandedToIt(t *testing.T) {
	in := desireInputs
	in.User = "1234:5678"
	d, err := Desire(desireProject(), "/dir", in)
	if err != nil {
		t.Fatal(err)
	}
	if findContainer(t, d, "proj-coder-1").User != "1234:5678" {
		t.Error("the agent does not run as the given user")
	}
	if findContainer(t, d, "proj-control-1").User != "" || findContainer(t, d, "proj-proxy-1").User != "" {
		t.Error("the sidecars must keep their own user")
	}
	if got := findContainer(t, d, "proj-coder-1").OwnedVolumes; !slices.Equal(got, []string{"proj_coder-home", "proj_shared"}) {
		t.Errorf("volumes handed to the agent = %v", got)
	}
	if got := findContainer(t, d, "proj-review-1").OwnedVolumes; len(got) != 0 {
		t.Errorf("a host-bound or custom agent was given volumes: %v", got)
	}
	if d.PrepImage != in.Image {
		t.Errorf("prep image = %q", d.PrepImage)
	}
}

func TestHarnessAgentsCarryTheirIntegrationInTheirDefinition(t *testing.T) {
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Harness = "claude-code"
	coder.Image = ""
	coder.Model = "claude-sonnet-5-5"
	coder.Permissions = "bypass"
	coder.Inject = config.ResolvedInject{HumanQuiet: "30s", AckTimeout: "60s", IdleSignal: "hook", Quiescence: "5s"}
	project.Agents["coder"] = coder
	d := desire(t, project)
	spec := findContainer(t, d, "proj-coder-1")
	for _, want := range []string{
		"EGZO_HARNESS=claude-code", "EGZO_MODEL=claude-sonnet-5-5", "EGZO_PERMISSIONS=bypass", "IS_SANDBOX=1",
		"EGZO_HUMAN_QUIET=30s", "EGZO_ACK_TIMEOUT=60s", "EGZO_IDLE_SIGNAL=hook", "EGZO_WORKSPACES=/workspace/shared",
	} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("env lacks %s\n%v", want, spec.Env)
		}
	}
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") && !strings.Contains(entry, "placeholder") {
			t.Errorf("a real-looking credential in the definition: %s", entry)
		}
	}
	coder.Permissions = "default"
	project.Agents["coder"] = coder
	if slices.Contains(findContainer(t, desire(t, project), "proj-coder-1").Env, "IS_SANDBOX=1") {
		t.Error("IS_SANDBOX is set although permissions are not bypassed")
	}
}

func TestAnAgentPromptIsMountedReadOnlyFromTheProjectDirectory(t *testing.T) {
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Prompt = "./prompts/coder.md"
	project.Agents["coder"] = coder
	spec := findContainer(t, desire(t, project), "proj-coder-1")
	want := MountSpec{Bind: true, Source: "/dir/prompts/coder.md", Target: "/etc/egzo/prompt.md", ReadOnly: true}
	if !slices.Contains(spec.Mounts, want) || !slices.Contains(spec.Env, "EGZO_PROMPT_FILE=/etc/egzo/prompt.md") {
		t.Errorf("mounts = %+v env = %v", spec.Mounts, spec.Env)
	}
}

func TestCustomAgentsGetNoHomeVolume(t *testing.T) {
	d := desire(t, desireProject())
	for _, volume := range d.Volumes {
		if volume.Name == "proj_review-home" {
			t.Error("a custom harness image has no egzo home")
		}
	}
}
