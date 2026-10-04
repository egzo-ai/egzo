package stack

import (
	"reflect"
	"testing"

	"github.com/egzo-ai/egzo/internal/engine"
)

func ctr(name, hash string) ContainerSpec {
	return ContainerSpec{Name: name, Identity: engine.Identity{ConfigHash: hash}}
}

func net(name, hash string) NetworkSpec {
	return NetworkSpec{Name: name, Identity: engine.Identity{ConfigHash: hash}}
}

func verbs(plan []Action) []string {
	out := make([]string, len(plan))
	for i, a := range plan {
		out[i] = a.String()
	}
	return out
}

func TestPlanCreatesEverythingOnAFreshEngine(t *testing.T) {
	desired := Desired{
		Attachments: []Attachment{{Container: "p-control-1", Alias: "control", Networks: []string{"p_coder"}}},
		Networks:    []NetworkSpec{net("p_control", "n"), net("p_coder", "n")},
		Volumes:     []VolumeSpec{{Name: "p_control"}},
		Containers:  []ContainerSpec{ctr("p-control-1", "c"), ctr("p-coder-1", "c")},
	}
	got := verbs(BuildPlan(desired, Observed{}, false))
	want := []string{
		"create network p_control", "create network p_coder", "create volume p_control",
		"create container p-control-1", "create container p-coder-1",
		"connect network p_coder to p-control-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v\nwant   %v", got, want)
	}
}

func TestPlanIsEmptyWhenNothingChanged(t *testing.T) {
	desired := Desired{
		Attachments: []Attachment{{Container: "p-control-1", Alias: "control", Networks: []string{"p_coder"}}},
		Networks:    []NetworkSpec{net("p_control", "n"), net("p_coder", "n")},
		Containers:  []ContainerSpec{ctr("p-control-1", "c")},
	}
	observed := Observed{Resources: []Resource{
		{Type: "network", Name: "p_control", ConfigHash: "n"},
		{Type: "network", Name: "p_coder", ConfigHash: "n"},
		{Type: "container", Name: "p-control-1", ConfigHash: "c", State: "running", Networks: []string{"p_control", "p_coder"}},
	}}
	if plan := BuildPlan(desired, observed, false); len(plan) != 0 {
		t.Errorf("plan = %v, want nothing", verbs(plan))
	}
}

func TestPlanRecreatesOnlyWhatChanged(t *testing.T) {
	desired := Desired{
		Containers: []ContainerSpec{ctr("p-control-1", "same"), ctr("p-coder-1", "new")},
	}
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-control-1", ConfigHash: "same", State: "running"},
		{Type: "container", Name: "p-coder-1", ConfigHash: "old", State: "running", ID: "id1"},
	}}
	got := verbs(BuildPlan(desired, observed, false))
	want := []string{"remove container p-coder-1", "create container p-coder-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestPlanNeverRemovesVolumes(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "volume", Name: "p_old-workspace", ID: "v1"},
		{Type: "volume", Name: "p_control", ID: "v2", ConfigHash: "stale"},
	}}
	desired := Desired{Volumes: []VolumeSpec{{Name: "p_control", Identity: engine.Identity{ConfigHash: "different"}}}}
	for _, action := range BuildPlan(desired, observed, false) {
		if action.Type == "volume" {
			t.Errorf("up must not touch existing volumes, got %s", action)
		}
	}
}

func TestPlanDisconnectsControlBeforeRemovingAnAgentNetwork(t *testing.T) {
	desired := Desired{Containers: []ContainerSpec{ctr("p-control-1", "c")}}
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-control-1", ConfigHash: "c", State: "running", Networks: []string{"p_control", "p_gone"}},
		{Type: "network", Name: "p_gone", ID: "n1"},
	}}
	got := verbs(BuildPlan(desired, observed, false))
	want := []string{"disconnect network p_gone from p-control-1", "remove network p_gone"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestPlanStartsAStoppedContainer(t *testing.T) {
	desired := Desired{Containers: []ContainerSpec{ctr("p-control-1", "c")}}
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-control-1", ConfigHash: "c", State: "exited", ID: "id"},
	}}
	got := verbs(BuildPlan(desired, observed, false))
	if !reflect.DeepEqual(got, []string{"start container p-control-1"}) {
		t.Errorf("plan = %v", got)
	}
}

func TestPlanRecreateFlagRecreatesContainersOnly(t *testing.T) {
	desired := Desired{
		Networks:   []NetworkSpec{net("p_control", "n")},
		Containers: []ContainerSpec{ctr("p-control-1", "c")},
	}
	observed := Observed{Resources: []Resource{
		{Type: "network", Name: "p_control", ConfigHash: "n"},
		{Type: "container", Name: "p-control-1", ConfigHash: "c", State: "running", ID: "id", Networks: []string{"p_control"}},
	}}
	got := verbs(BuildPlan(desired, observed, true))
	want := []string{"remove container p-control-1", "create container p-control-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestPlanAttachesEverySidecarAndDetachesThemFromRemovedNetworks(t *testing.T) {
	desired := Desired{
		Attachments: []Attachment{
			{Container: "p-control-1", Alias: "control", Networks: []string{"p_coder"}},
			{Container: "p-proxy-1", Alias: "proxy", Networks: []string{"p_coder"}},
		},
		Networks:   []NetworkSpec{net("p_coder", "n")},
		Containers: []ContainerSpec{ctr("p-control-1", "c"), ctr("p-proxy-1", "c")},
	}
	observed := Observed{Resources: []Resource{
		{Type: "network", Name: "p_coder", ConfigHash: "n"},
		{Type: "network", Name: "p_gone", ID: "n1"},
		{Type: "container", Name: "p-control-1", ConfigHash: "c", State: "running", Networks: []string{"p_coder", "p_gone"}},
		{Type: "container", Name: "p-proxy-1", ConfigHash: "c", State: "running", Networks: []string{"p_gone"}},
	}}
	got := verbs(BuildPlan(desired, observed, false))
	want := []string{
		"disconnect network p_gone from p-control-1", "disconnect network p_gone from p-proxy-1",
		"remove network p_gone",
		"connect network p_coder to p-proxy-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v\nwant   %v", got, want)
	}
}

func TestOwnershipRefusesAnotherDirectory(t *testing.T) {
	observed := Observed{Resources: []Resource{{Type: "container", Name: "x", ProjectDir: "/other"}}}
	if err := CheckOwnership(observed, "p", "/here"); err == nil {
		t.Fatal("same name from another directory was accepted")
	}
	if err := CheckOwnership(observed, "p", "/other"); err != nil {
		t.Fatalf("same directory refused: %v", err)
	}
	if err := CheckOwnership(Observed{}, "p", "/here"); err != nil {
		t.Fatalf("empty project refused: %v", err)
	}
}
