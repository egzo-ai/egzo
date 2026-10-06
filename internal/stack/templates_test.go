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
)

func templateProject(t *testing.T) (*config.Resolved, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("be brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := desireProject()
	project.Egress = map[string]*config.ResolvedProfile{"default": {Allow: []string{"pypi.org"}}}
	coder := project.Agents["coder"]
	coder.Prompt = "p.md"
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
	if coder.PromptPath != filepath.Join(dir, "p.md") || coder.PromptDigest == "" {
		t.Errorf("the prompt is not resolved: %+v", coder)
	}
	if !strings.HasPrefix(coder.Image, "ghcr.io/egzo-ai/egzo-harness-claude-code:") || published.Templates["review"].Image != "example/review:1" {
		t.Errorf("images = %q, %q", coder.Image, published.Templates["review"].Image)
	}
	if _, ok := coder.Workspaces["shared"]; !ok || len(published.Templates["review"].Workspaces) != 0 {
		t.Errorf("a template carries the declared workspaces it lists and no host path: %+v / %+v", coder.Workspaces, published.Templates["review"].Workspaces)
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
			p.Workspaces["shared"] = config.ResolvedWorkspace{Git: &config.Git{URL: "https://x/y"}, Mode: "clone", Path: "/p"}
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
	if agent.Prompt != filepath.Join(dir, "p.md") || agent.Image != published.Templates["coder"].Image {
		t.Errorf("prompt and image are not the resolved ones: %+v", agent)
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
	d, err := DesireInstance(view, dir, in, "issue-1", InstanceIdentity{Template: "coder", Actor: "operator", TemplateHash: template.Hash})
	if err != nil {
		t.Fatal(err)
	}
	spec := findContainer(t, d, "proj-issue-1")
	if spec.Network != "proj_issue-1" || spec.User != "1000:1000" || !spec.Init || spec.ReadonlyRootfs {
		t.Errorf("spec = %+v", spec)
	}
	for _, want := range []string{"EGZO_AGENT=issue-1", "EGZO_TOKEN=tok", "HTTPS_PROXY=http://issue-1:tok@proxy:3128"} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("env lacks %s: %v", want, spec.Env)
		}
	}
	if want := (MountSpec{Bind: true, Source: filepath.Join(dir, "p.md"), Target: "/etc/egzo/prompt.md", ReadOnly: true}); !slices.Contains(spec.Mounts, want) {
		t.Errorf("mounts = %+v", spec.Mounts)
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
	if err == nil || !strings.Contains(err.Error(), "b, c") || !strings.Contains(err.Error(), "egzo prune --stale") {
		t.Errorf("err = %v", err)
	}
}

func TestFreeNameTakesTheFirstFreeNumber(t *testing.T) {
	inst := func(name string) Resource {
		return Resource{Type: "container", Name: "proj-" + name, Kind: kindAgent, Service: "coder", Instance: name}
	}
	if got := freeName(Observed{}, "coder"); got != "coder-1" {
		t.Errorf("first = %q", got)
	}
	if got := freeName(Observed{Resources: []Resource{inst("coder-1"), inst("coder-3"), inst("other-2")}}, "coder"); got != "coder-2" {
		t.Errorf("with a gap = %q", got)
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
		{Type: "volume", Name: "proj_kept-home", Kind: kindAgent, Service: "coder", Instance: "kept"}, // kept by `down`: not an instance
		{Type: "volume", Name: "proj_shared", Kind: kindWorkspace, Service: "shared"},
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
		removed, err := RemoveInstances(context.Background(), nil, observed, "proj", names, false, io.Discard)
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
		if !actorName.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "root", "user:", "user:a b", "service:" + strings.Repeat("x", 64), "operator\n", "agent:coder", "user:\x1b[2J"} {
		if actorName.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
