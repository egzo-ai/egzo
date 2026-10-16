// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// --- R-30: a container that was created and never started is recreated, not just started -------------------

func TestAContainerThatWasCreatedButNeverStartedIsRecreated(t *testing.T) {
	desired := Desired{Containers: []ContainerSpec{ctr("p-coder-1", "c")}}
	observed := Observed{Resources: []Resource{{Type: "container", Name: "p-coder-1", ID: "id1", ConfigHash: "c", State: "created"}}}
	got := verbs(BuildPlan(desired, observed, false))
	want := []string{"remove container p-coder-1", "create container p-coder-1"}
	if !slices.Equal(got, want) {
		t.Errorf("plan = %v, want %v: a plain start would skip the volume hand-over the interrupted run never did", got, want)
	}
	observed.Resources[0].State = "exited"
	if got := verbs(BuildPlan(desired, observed, false)); !slices.Equal(got, []string{"start container p-coder-1"}) {
		t.Errorf("an exited container that ran before is just started: %v", got)
	}
}

// --- instances are not converged: up leaves them alone ------------------------------------------------------------

func TestUpNeverRemovesOrChangesInstances(t *testing.T) {
	project := desireProject()
	desired, err := Desire(project, "/dir", Inputs{Image: "egzo:test", Instances: []string{"coder-1"}})
	if err != nil {
		t.Fatal(err)
	}
	infra := BuildPlan(desired, Observed{}, false)
	// Everything the infrastructure needs exists, plus an instance with its network, home and container.
	var observed Observed
	for _, action := range infra {
		if action.Verb == "create" {
			hash := ""
			for _, c := range desired.Containers {
				if c.Name == action.Name {
					hash = c.Identity.ConfigHash
				}
			}
			for _, n := range desired.Networks {
				if n.Name == action.Name {
					hash = n.Identity.ConfigHash
				}
			}
			observed.Resources = append(observed.Resources, Resource{Type: action.Type, Name: action.Name, ID: action.Name, ConfigHash: hash, State: "running", Networks: []string{"proj_coder-1"}})
		}
	}
	observed.Resources = append(observed.Resources,
		Resource{Type: "container", Name: "proj-coder-1", ID: "c", Kind: kindAgent, Service: "coder", Instance: "coder-1", State: "running", ConfigHash: "anything"},
		Resource{Type: "network", Name: "proj_coder-1", ID: "n", Kind: kindAgent, Service: "coder", Instance: "coder-1"},
		Resource{Type: "volume", Name: "proj_coder-1-home", ID: "v", Kind: kindAgent, Service: "coder", Instance: "coder-1"},
	)
	for _, action := range BuildPlan(desired, observed, true) {
		if action.Name == "proj-coder-1" || action.Name == "proj_coder-1" || action.Name == "proj_coder-1-home" {
			if action.Verb != "connect" {
				t.Errorf("up touches an instance: %s", action)
			}
		}
	}
	// A recreated sidecar is attached to the networks of the instances again.
	var connects []string
	for _, action := range BuildPlan(desired, observed, true) {
		if action.Verb == "connect" {
			connects = append(connects, action.String())
		}
	}
	if len(connects) != 2 {
		t.Errorf("a recreated sidecar was not attached to the instance's network: %v", connects)
	}
}

// reaches reports whether action `from` waits, directly or not, for action `to`.
func reaches(deps [][]int, from, to int) bool {
	seen := map[int]bool{}
	var walk func(int) bool
	walk = func(i int) bool {
		for _, d := range deps[i] {
			if d == to || (!seen[d] && walk(d)) {
				return true
			}
			seen[d] = true
		}
		return false
	}
	return walk(from)
}

func TestTheDependencyGraphOfARealProjectHasNoCycle(t *testing.T) {
	project := desireProject()
	desired := desire(t, project)
	plan := BuildPlan(desired, Observed{}, false)
	deps := dependencies(desired, plan)
	for i := range plan {
		if reaches(deps, i, i) {
			t.Errorf("%s waits for itself", plan[i])
		}
	}
}

// --- R-27: limits and unprivileged sidecars ----------------------------------------------------------------------

func TestSidecarsRunUnprivilegedWithLimits(t *testing.T) {
	d := desire(t, desireProject())
	for _, name := range []string{"proj-control-1", "proj-proxy-1"} {
		c := findContainer(t, d, name)
		if c.User == "" || c.User == "0" || strings.HasPrefix(c.User, "0:") || strings.HasPrefix(c.User, "root") {
			t.Errorf("%s runs as %q", name, c.User)
		}
		if c.Memory <= 0 || c.PidsLimit <= 0 {
			t.Errorf("%s limits: memory %d pids %d", name, c.Memory, c.PidsLimit)
		}
		if len(c.OwnedVolumes) == 0 {
			t.Errorf("%s writes volumes it does not own", name)
		}
	}
	control := findContainer(t, d, "proj-control-1")
	if !slices.Contains(control.OwnedVolumes, "proj_control") {
		t.Errorf("control volumes = %v", control.OwnedVolumes)
	}
	proxy := findContainer(t, d, "proj-proxy-1")
	if !slices.Contains(proxy.OwnedVolumes, "proj_ca") || !slices.Contains(proxy.OwnedVolumes, "proj_ca-private") {
		t.Errorf("proxy volumes = %v", proxy.OwnedVolumes)
	}
	for target, options := range proxy.Tmpfs {
		// writable by the sidecar's user: owned by it, or open to everyone in the container (Podman refuses the uid option)
		if !strings.Contains(options, "uid="+strings.Split(proxy.User, ":")[0]) && !strings.Contains(options, "mode=1777") {
			t.Errorf("tmpfs %s (%s) is not writable by %s", target, options, proxy.User)
		}
	}
}

func TestAgentsGetAProcessLimitUnlessTheFileSetsOne(t *testing.T) {
	d := desire(t, desireProject())
	if c := findContainer(t, d, "proj-coder"); c.PidsLimit != defaultAgentPids {
		t.Errorf("pids = %d", c.PidsLimit)
	}
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Resources = config.Resources{Pids: 77, Memory: "1g"}
	project.Agents["coder"] = coder
	c := findContainer(t, desire(t, project), "proj-coder")
	if c.PidsLimit != 77 {
		t.Errorf("pids = %d", c.PidsLimit)
	}
	if c.MemorySwap != c.Memory || c.Memory == 0 {
		t.Errorf("memory %d swap %d: a memory limit must not be doubled by swap", c.Memory, c.MemorySwap)
	}
}

// --- R-29: who agents run as ----------------------------------------------------------------------------------------

func TestWhoAgentsRunAs(t *testing.T) {
	cases := []struct {
		name     string
		client   engine.Client
		uid, gid int
		want     string
	}{
		{"an ordinary user", engine.Client{}, 1000, 1000, "1000:1000"},
		{"root is never an agent's user", engine.Client{}, 0, 0, "1000:1000"},
		{"podman maps users itself", engine.Client{Podman: true}, 1000, 1000, ""},
		{"rootless docker maps users itself", engine.Client{Rootless: true}, 1000, 1000, ""},
		{"no uid on this platform", engine.Client{}, -1, -1, ""},
	}
	for _, c := range cases {
		if got := agentUser(&c.client, c.uid, c.gid); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// --- R-39: the content of the prompt is part of what the agent is ---------------------------------------------------

func TestChangingThePromptChangesTheAgentsHash(t *testing.T) {
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Prompt = "./prompts/coder.md"
	project.Agents["coder"] = coder
	hashOf := func(text string) string {
		in := desireInputs
		in.Prompts = map[string]string{"coder": text}
		d, err := desireErr(project, "/dir", in)
		if err != nil {
			t.Fatal(err)
		}
		return findContainer(t, d, "proj-coder").Identity.ConfigHash
	}
	if hashOf("aaa") == hashOf("bbb") {
		t.Error("editing the prompt file did not change the agent's hash: `up` would leave a stale agent running")
	}
	first, second := hashOf("aaa"), hashOf("aaa")
	if first != second {
		t.Error("the hash is not stable")
	}
}

func TestReadPromptsReadTheFilesAndFailForAMissingOrOversizedOne(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("be brief"), 0o644)
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Prompt = "p.md"
	project.Agents["coder"] = coder
	prompts, err := ReadPrompts(project, dir)
	if err != nil || prompts["coder"] != "be brief" || prompts["review"] != "" {
		t.Fatalf("prompts = %v, err = %v", prompts, err)
	}
	os.WriteFile(filepath.Join(dir, "p.md"), make([]byte, config.MaxPromptBytes+1), 0o644)
	if _, err := ReadPrompts(project, dir); err == nil || !strings.Contains(err.Error(), "64 KB") {
		t.Errorf("an oversized prompt: %v", err)
	}
	os.Remove(filepath.Join(dir, "p.md"))
	if _, err := ReadPrompts(project, dir); err == nil {
		t.Error("a missing prompt file was not reported")
	}
}

// --- R-28: pulling images --------------------------------------------------------------------------------------------

func TestAPullStreamThatReportsAnErrorIsAnError(t *testing.T) {
	ok := `{"status":"Pulling from library/alpine"}` + "\n" + `{"status":"Download complete","id":"abc"}` + "\n"
	if err := checkPullStream(strings.NewReader(ok)); err != nil {
		t.Errorf("a good pull: %v", err)
	}
	failed := ok + `{"errorDetail":{"message":"manifest unknown"},"error":"manifest for x:1 not found: manifest unknown"}` + "\n"
	if err := checkPullStream(strings.NewReader(failed)); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("a failed pull was reported as %v", err)
	}
	if err := checkPullStream(strings.NewReader("not json\n")); err == nil {
		t.Error("garbage was accepted as a finished pull")
	}
}

func TestRegistryCredentialsComeFromTheDockerConfig(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	config := []byte(`{"auths":{"ghcr.io":{"auth":"` + auth + `"},"https://index.docker.io/v1/":{"auth":"` + auth + `"}}}`)
	cases := map[string]bool{
		"ghcr.io/egzo-ai/egzo:1": true, "alpine:3": true, "docker.io/library/alpine:3": true, "quay.io/x/y:1": false, "localhost:5000/x:1": false,
	}
	for ref, wantAuth := range cases {
		got := registryAuth(ref, config)
		if (got != "") != wantAuth {
			t.Errorf("%s: auth present = %v, want %v", ref, got != "", wantAuth)
			continue
		}
		if got != "" {
			raw, err := base64.URLEncoding.DecodeString(got)
			var decoded struct{ Username, Password string }
			if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Username != "alice" || decoded.Password != "s3cret" {
				t.Errorf("%s: auth = %s (%v)", ref, raw, err)
			}
		}
	}
	if registryAuth("ghcr.io/x:1", []byte("not json")) != "" {
		t.Error("a broken config produced credentials")
	}
}

// --- R-06: nothing an agent wrote reaches the terminal as a control sequence ----------------------------------------

func TestPsShowsAnAgentsStatusAsOneHarmlessLine(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-coder-1", Service: "coder", Instance: "coder-1", Kind: kindAgent, State: "running"},
		{Type: "container", Name: "p-proxy-1", Service: "proxy", Kind: kindProxy, State: "running"},
	}}
	var out bytes.Buffer
	WriteStatus(Rows(observed, map[string]Report{"coder-1": {Status: "hi\n\x1b]52;c;ZXZpbA==\x07p-fake-1  fake  running\x1b[2J", Activity: "idle"}}, nil), time.Now(), &out)
	if strings.ContainsAny(out.String(), "\x1b\x07\x00") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 {
		t.Errorf("a status line forged rows: %q", out.String())
	}
}
