// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/names"
)

func templateProject(t *testing.T) (*config.Resolved, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("be brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := desireProject()
	project.Egress = map[string]*config.ResolvedProfile{"default": {Allow: []string{"pypi.org"}}}
	project.Workspaces["repo"] = config.ResolvedWorkspace{Git: &config.Git{URL: "https://github.com/acme/shop.git"}}
	coder := project.Agents["coder"]
	coder.Prompt = "p.md"
	coder.Workspaces = append(coder.Workspaces, config.Mount{Name: "repo", Mount: "/workspace/repo", Mode: "rw"})
	project.Agents["coder"] = coder
	return project, dir
}

func publish(t *testing.T, project *config.Resolved, dir string) Published {
	t.Helper()
	published, err := Publish(project, dir, "egzo:test", "", "1000:1000")
	if err != nil {
		t.Fatal(err)
	}
	return published
}

func TestPublishResolvesEveryTemplateWithWhatAnInstanceNeeds(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	if !slices.Equal(published.Names(), []string{"coder", "review"}) {
		t.Fatalf("templates = %v", published.Names())
	}
	coder := published.Templates["coder"]
	if coder.PromptText != "be brief" || coder.Agent.Prompt != "" {
		t.Errorf("the prompt travels as text, and its path means nothing to a spawn: %+v", coder)
	}
	if !strings.HasPrefix(coder.Image, "ghcr.io/egzo-ai/egzo-harness-claude-code:") || published.Templates["review"].Image != "example/review:1" {
		t.Errorf("images = %q, %q", coder.Image, published.Templates["review"].Image)
	}
	if _, ok := coder.Workspaces["repo"]; !ok || len(coder.Workspaces) != 2 || len(published.Templates["review"].Workspaces) != 1 {
		t.Errorf("a template carries the workspaces it lists: %+v / %+v", coder.Workspaces, published.Templates["review"].Workspaces)
	}
	if coder.Profile == nil || published.User != "1000:1000" || published.Image != "egzo:test" || published.Dir != dir {
		t.Errorf("published = %+v", published)
	}
	if coder.Hash == "" || coder.Hash == published.Templates["review"].Hash {
		t.Errorf("hashes = %q, %q", coder.Hash, published.Templates["review"].Hash)
	}
}

func TestPublishFailsForAPromptItCannotRead(t *testing.T) {
	project, dir := templateProject(t)
	os.Remove(filepath.Join(dir, "p.md"))
	if _, err := Publish(project, dir, "egzo:test", "", ""); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Errorf("err = %v", err)
	}
}

func TestATemplatesHashFollowsWhatMakesTheInstanceAndNothingElse(t *testing.T) {
	project, dir := templateProject(t)
	base := publish(t, project, dir).Templates["coder"].Hash
	if again := publish(t, project, dir).Templates["coder"].Hash; again != base {
		t.Error("the hash is not stable")
	}
	for name, change := range map[string]func(*config.Resolved){
		"env": func(p *config.Resolved) {
			a := p.Agents["coder"]
			a.Env = map[string]string{"X": "1"}
			p.Agents["coder"] = a
		},
		"model":   func(p *config.Resolved) { a := p.Agents["coder"]; a.Model = "m"; p.Agents["coder"] = a },
		"profile": func(p *config.Resolved) { p.Egress["default"].Allow = append(p.Egress["default"].Allow, "x.example") },
		"workspace": func(p *config.Resolved) {
			p.Workspaces["repo"] = config.ResolvedWorkspace{Git: &config.Git{URL: "https://x/y"}}
		},
	} {
		other, otherDir := templateProject(t)
		change(other)
		if got := publish(t, other, otherDir).Templates["coder"].Hash; got == base {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
	// the other template does not follow the coder's changes
	other, otherDir := templateProject(t)
	a := other.Agents["coder"]
	a.Env = map[string]string{"X": "1"}
	other.Agents["coder"] = a
	if publish(t, other, otherDir).Templates["review"].Hash != publish(t, project, dir).Templates["review"].Hash {
		t.Error("an unrelated template's hash moved")
	}
	// a changed prompt file changes the hash; where the project directory is does not
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("be verbose"), 0o644)
	if publish(t, project, dir).Templates["coder"].Hash == base {
		t.Error("editing the prompt file did not change the hash")
	}
}

func TestTheTemplatesSurviveTheTripThroughTheControlSidecar(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	data, err := published.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePublished(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range published.Names() {
		if back.Templates[name].Hash != published.Templates[name].Hash || templateHash(back.Templates[name]) != published.Templates[name].Hash {
			t.Errorf("%s changed on the way", name)
		}
	}
	again, _ := back.Marshal()
	if string(again) != string(data) {
		t.Error("marshalling is not stable: `up` would republish the templates every time")
	}
	if _, err := ParsePublished([]byte(`{"Version":99}`)); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("an unknown version: %v", err)
	}
	if _, err := ParsePublished([]byte(`nope`)); err == nil {
		t.Error("garbage was accepted")
	}
}

func TestAViewRunsTheInstanceUnderItsOwnNameFromItsTemplate(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	view := published.View(map[string]string{"issue-1": "coder", "issue-2": "coder", "ghost": "missing"})
	if !slices.Equal(sortedKeys(view.Agents), []string{"issue-1", "issue-2"}) {
		t.Fatalf("agents = %v", sortedKeys(view.Agents))
	}
	agent := view.Agents["issue-1"]
	if agent.Prompt != "" || agent.Image != published.Templates["coder"].Image {
		t.Errorf("the image is not the resolved one, or a prompt path is left: %+v", agent)
	}
	if view.Name != "proj" || view.Egress["default"] == nil || view.Workspaces["shared"].Git != nil {
		t.Errorf("view = %+v", view)
	}
}

func TestAnInstanceIsDesiredFromItsTemplateAndCarriesItsOrigin(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	instances := map[string]string{"issue-1": "coder"}
	view := published.View(instances)
	in := published.inputs(instances, map[string]string{"issue-1": "tok"}, nil)
	template := published.Templates["coder"]
	d, err := DesireInstance(view, dir, in, "issue-1", InstanceIdentity{ID: "01JBX7Q0000000000000000000", Template: "coder", Actor: "operator", TemplateHash: template.Hash})
	if err != nil {
		t.Fatal(err)
	}
	spec := findContainer(t, d, "proj-issue-1")
	// The id is on what is this instance's alone; the home can be taken up by the next instance of the name.
	if got := spec.Identity.Labels()[engine.LabelInstanceID]; got != "01JBX7Q0000000000000000000" {
		t.Errorf("the container's instance id = %q", got)
	}
	if got := d.Networks[0].Identity.Labels()[engine.LabelInstanceID]; got != "01JBX7Q0000000000000000000" {
		t.Errorf("the network's instance id = %q", got)
	}
	for _, volume := range d.Volumes {
		if got, ok := volume.Identity.Labels()[engine.LabelInstanceID]; ok {
			t.Errorf("the volume %s has the instance id %q", volume.Name, got)
		}
	}
	if spec.Network != "proj_issue-1" || spec.User != "1000:1000" || !spec.Init || spec.ReadonlyRootfs {
		t.Errorf("spec = %+v", spec)
	}
	for _, want := range []string{"EGZO_AGENT=issue-1", "EGZO_TOKEN=tok", "HTTPS_PROXY=http://issue-1:tok@proxy:3128"} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("env lacks %s: %v", want, spec.Env)
		}
	}
	if !slices.Contains(spec.Env, "EGZO_PROMPT=be brief") {
		t.Errorf("the prompt is not in the environment: %v", spec.Env)
	}
	for _, m := range spec.Mounts {
		if m.Bind {
			t.Errorf("a host path is mounted: %+v", m)
		}
	}
	if want := (MountSpec{Source: "proj_issue-1-home", Target: "/home/agent"}); !slices.Contains(spec.Mounts, want) {
		t.Errorf("the home volume is the instance's: %+v", spec.Mounts)
	}
	labels := spec.Identity.Labels()
	for key, want := range map[string]string{
		engine.LabelKind: "agent", engine.LabelService: "coder", engine.LabelInstance: "issue-1",
		engine.LabelActor: "operator", engine.LabelTemplateHash: template.Hash,
	} {
		if labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if len(d.Networks) != 1 || d.Networks[0].Name != "proj_issue-1" || !d.Networks[0].Internal ||
		d.Networks[0].Identity.Labels()[engine.LabelInstance] != "issue-1" {
		t.Errorf("networks = %+v", d.Networks)
	}
	if len(d.Volumes) != 1 || d.Volumes[0].Name != "proj_issue-1-home" || d.Volumes[0].Identity.Labels()[engine.LabelInstance] != "issue-1" {
		t.Errorf("volumes = %+v", d.Volumes)
	}
	if len(d.Attachments) != 2 || d.Attachments[0].Container != "proj-control-1" || d.Attachments[1].Container != "proj-proxy-1" ||
		!slices.Equal(d.Attachments[0].Networks, []string{"proj_issue-1"}) {
		t.Errorf("attachments = %+v", d.Attachments)
	}
	if _, err := DesireInstance(view, dir, in, "nobody", InstanceIdentity{}); err == nil {
		t.Error("an instance that is not in the view was desired")
	}
}

func TestTwoInstancesOfATemplateShareItsHashAndNothingElse(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	spawn := func(name string) ContainerSpec {
		instances := map[string]string{name: "coder"}
		d, err := DesireInstance(published.View(instances), dir, published.inputs(instances, map[string]string{name: "tok-" + name}, nil), name,
			InstanceIdentity{Template: "coder", Actor: "operator", TemplateHash: published.Templates["coder"].Hash})
		if err != nil {
			t.Fatal(err)
		}
		return d.Containers[0]
	}
	one, two := spawn("one"), spawn("two")
	if one.Name == two.Name || one.Network == two.Network || one.Identity.Labels()[engine.LabelTemplateHash] != two.Identity.Labels()[engine.LabelTemplateHash] {
		t.Errorf("one = %+v two = %+v", one, two)
	}
	for _, m := range one.Mounts {
		if strings.Contains(m.Source, "two") {
			t.Errorf("one mounts something of two: %+v", m)
		}
	}
}

func TestInstancesAreListedFromTheLabelsAndStaleWhenTheirTemplateMoved(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	hash := published.Templates["coder"].Hash
	created := time.Now().Add(-time.Hour)
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "proj-control-1", Kind: kindControl, Service: "control", State: "running"},
		{Type: "container", Name: "proj-b", Kind: kindAgent, Service: "coder", Instance: "b", Actor: "operator", State: "exited", TemplateHash: "old", Created: created},
		{Type: "container", Name: "proj-a", Kind: kindAgent, Service: "coder", Instance: "a", Actor: "user:ann", State: "running", TemplateHash: hash, Created: created},
		{Type: "container", Name: "proj-c", Kind: kindAgent, Service: "gone", Instance: "c", State: "running", TemplateHash: "x"},
		{Type: "network", Name: "proj_a", Kind: kindAgent, Service: "coder", Instance: "a"},
	}}
	instances := observed.Instances()
	if got := observed.InstanceNames(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("instances = %v", got)
	}
	if instances[0].Actor != "user:ann" || instances[0].Template != "coder" || instances[0].Container != "proj-a" {
		t.Errorf("a = %+v", instances[0])
	}
	if _, ok := observed.Instance("control"); ok {
		t.Error("a sidecar is not an instance")
	}
	var stale []string
	for _, instance := range StaleInstances(observed, published) {
		stale = append(stale, instance.Name)
	}
	if !slices.Equal(stale, []string{"b", "c"}) {
		t.Errorf("stale = %v, want the one whose hash moved and the one whose template is gone", stale)
	}
	err := StaleError(StaleInstances(observed, published))
	if err == nil || !strings.Contains(err.Error(), "b, c") || !strings.Contains(err.Error(), "egzo agent rm --stale") {
		t.Errorf("err = %v", err)
	}
}

func TestFreeNameTakesTheFirstNumberNoInstanceOrWorktreeVolumeHas(t *testing.T) {
	inst := func(name string) Resource {
		return Resource{Type: "container", Name: "proj-" + name, Kind: kindAgent, Service: "coder", Instance: name}
	}
	// with no git workspace the engine is never asked, so no client is needed
	if got, err := freeName(context.Background(), nil, PrepEnv{}, Observed{}, "proj", "/p", "coder", nil); err != nil || got != "coder-1" {
		t.Errorf("first = %q, %v", got, err)
	}
	observed := Observed{Resources: []Resource{inst("coder-1"), inst("coder-3"), inst("other-2"),
		{Type: "volume", Name: "proj_ws.repo.wt.coder-2", Kind: kindWorkspace, Workspace: "repo", Instance: "coder-2"}}}
	if got, err := freeName(context.Background(), nil, PrepEnv{}, observed, "proj", "/p", "coder", nil); err != nil || got != "coder-4" {
		t.Errorf("the gap at 2 holds a worktree volume, so it is not free: %q, %v", got, err)
	}
}

func TestRowsMarkStaleInstancesAndAgeIsReadable(t *testing.T) {
	project, dir := templateProject(t)
	published := publish(t, project, dir)
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "proj-a", Kind: kindAgent, Service: "coder", Instance: "a", State: "running", TemplateHash: published.Templates["coder"].Hash},
		{Type: "container", Name: "proj-b", Kind: kindAgent, Service: "coder", Instance: "b", State: "running", TemplateHash: "old"},
		{Type: "container", Name: "proj-proxy-1", Kind: kindProxy, Service: "proxy", State: "running"},
	}}
	rows := Rows(observed, nil, &published)
	if rows[0].Name != "a" || rows[0].Stale || !rows[1].Stale || rows[2].Name != "proj-proxy-1" || rows[2].Stale {
		t.Errorf("rows = %+v", rows)
	}
	if rows := Rows(observed, nil, nil); rows[1].Stale {
		t.Error("staleness was claimed without templates to compare with")
	}
	for d, want := range map[time.Duration]string{5 * time.Second: "5s", 7 * time.Minute: "7m", 3 * time.Hour: "3h", 49 * time.Hour: "2d", -time.Second: ""} {
		if got := Age(d); got != want {
			t.Errorf("Age(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestWhatIsLeftOfAnInstanceWithoutItsContainerIsStillAnInstanceToRemove(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "proj-a", Kind: kindAgent, Service: "coder", Instance: "a", State: "running"},
		{Type: "network", Name: "proj_a", Kind: kindAgent, Service: "coder", Instance: "a"},
		{Type: "network", Name: "proj_b", Kind: kindAgent, Service: "coder", Instance: "b", Actor: "operator"},
		{Type: "volume", Name: "proj_b-home", Kind: kindAgent, Service: "coder", Instance: "b"},
		{Type: "network", Name: "proj_c", Kind: kindAgent, Service: "review", Instance: "c"},
		{Type: "volume", Name: "proj_kept-home", Kind: kindAgent, Service: "coder", Instance: "kept"}, // no container, no network: not an instance
	}}
	got := observed.Instances()
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("instances = %+v", got)
	}
	if got[0].State != "running" || got[1].State != "missing" || got[1].Template != "coder" || got[2].Template != "review" {
		t.Errorf("states and templates = %+v", got)
	}
}

func TestRemovingChecksEveryNameBeforeTouchingAnything(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "proj-a", Kind: kindAgent, Service: "coder", Instance: "a", State: "exited"},
		{Type: "container", Name: "proj-b", Kind: kindAgent, Service: "coder", Instance: "b", State: "running"},
	}}
	// a nil client: anything that reaches the engine panics, so these must fail first
	for name, names := range map[string][]string{"an unknown name": {"a", "ghost"}, "a running instance": {"a", "b"}} {
		removed, err := RemoveInstances(context.Background(), nil, observed, PrepEnv{}, "proj", "/p", names, false, io.Discard)
		if err == nil || len(removed) != 0 {
			t.Errorf("%s: removed %v, err %v", name, removed, err)
		}
	}
}

func TestUndoRunsInReverseOrder(t *testing.T) {
	var undo undoStack
	var order []int
	for i := 1; i <= 3; i++ {
		undo.add(func(context.Context) { order = append(order, i) })
	}
	if undo.empty() {
		t.Error("an undo with steps is empty")
	}
	undo.run(context.Background())
	if !slices.Equal(order, []int{3, 2, 1}) {
		t.Errorf("order = %v", order)
	}
	if !(&undoStack{}).empty() {
		t.Error("a new undo is not empty")
	}
}

func TestOnlyPlainActorsAreWrittenToLabels(t *testing.T) {
	for _, ok := range []string{"operator", "user:ann", "user:ann@example.org", "service:github-webhooks"} {
		if !names.Actor(ok, "user", "service") {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "root", "user:", "user:a b", "service:" + strings.Repeat("x", 129), "operator\n", "agent:coder", "user:\x1b[2J"} {
		if names.Actor(bad, "user", "service") {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLocalPortsAndTheHubDecideTheStalenessOfATemplateWithPortsOnly(t *testing.T) {
	project, dir := templateProject(t)
	coder := project.Agents["coder"]
	coder.Ports = map[string]config.ResolvedPort{"web": {Port: 5173, Protocol: "http"}}
	project.Agents["coder"] = coder
	project.Hub = &config.ResolvedHub{URL: "https://hub.example.org"}
	before := publish(t, project, dir)
	if !before.Templates["coder"].Hub || before.Templates["review"].Hub {
		t.Errorf("only a template with ports is told whether a hub is named: %+v", before.Templates)
	}
	project.LocalPorts = &config.ResolvedLocalPorts{Bind: "127.0.0.1", Host: "127.0.0.1", PoolLow: 20000, PoolHigh: 29999, MaxExposed: 10}
	after := publish(t, project, dir)
	if before.Templates["coder"].Hash == after.Templates["coder"].Hash {
		t.Error("a project that turns local ports on leaves the instances of a template with ports as they are")
	}
	if before.Templates["review"].Hash != after.Templates["review"].Hash {
		t.Error("a template without ports went stale")
	}
	view := after.View(map[string]string{"coder-1": "coder"})
	if view.LocalPorts == nil || view.Hub == nil || len(view.Agents["coder-1"].Ports) != 1 {
		t.Errorf("the view an instance is made from lacks them: %+v", view)
	}
	if as := after.AsProject(nil); as.LocalPorts != nil || as.Hub != nil {
		t.Error("the project as the templates describe it names no hub: the link is not made from it")
	}
}

func TestPsShowsTheNameAndWhereOfEachOpenPort(t *testing.T) {
	observed := Observed{Resources: []Resource{{Type: "container", Name: "p-coder-1", Kind: kindAgent, Instance: "coder-1", State: "running", Service: "coder"}}}
	var out strings.Builder
	WriteStatus(Rows(observed, map[string]Report{"coder-1": {Activity: "idle", Exposed: []Exposure{{Name: "web", Where: "hub", Port: 5173, Protocol: "http"}, {Name: "db", Where: "local", Port: 5432, Protocol: "tcp"}}}}, nil), time.Now(), &out)
	if !strings.Contains(out.String(), "web:hub,db:local") {
		t.Errorf("%s", out.String())
	}
}
