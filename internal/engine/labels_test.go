package engine

import (
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/version"
)

func TestLabels(t *testing.T) {
	labels := Identity{
		Project:    "proj",
		Service:    "coder",
		Kind:       "agent",
		ProjectDir: "/work/proj",
		ConfigHash: "abc123",
	}.Labels()

	want := map[string]string{
		"ai.egzo.project":      "proj",
		"ai.egzo.service":      "coder",
		"ai.egzo.kind":         "agent",
		"ai.egzo.config-hash":  "abc123",
		"ai.egzo.spec-version": SpecVersion,
		"ai.egzo.project-dir":  "/work/proj",
		"ai.egzo.created-by":   "egzo/" + version.Version,
	}
	if len(labels) != len(want) {
		t.Errorf("labels = %v, want exactly %v", labels, want)
	}
	for key, value := range want {
		if labels[key] != value {
			t.Errorf("label %s = %q, want %q", key, labels[key], value)
		}
	}
}

func TestLabelsIncludeExtras(t *testing.T) {
	labels := Identity{Project: "p", Extra: map[string]string{LabelAgentHarness: "pi"}}.Labels()
	if labels[LabelAgentHarness] != "pi" {
		t.Errorf("extra label missing: %v", labels)
	}
}

func TestAnInstanceCarriesItsNameActorAndTemplateHash(t *testing.T) {
	labels := Identity{Project: "p", Service: "coder", Kind: "agent", Instance: "issue-1", Actor: "operator", TemplateHash: "th"}.Labels()
	for key, want := range map[string]string{LabelService: "coder", LabelInstance: "issue-1", LabelActor: "operator", LabelTemplateHash: "th"} {
		if labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if plain := (Identity{Project: "p", Service: "control", Kind: "control"}).Labels(); plain[LabelInstance] != "" || plain[LabelActor] != "" {
		t.Errorf("a sidecar has instance labels: %v", plain)
	}
}

// The label keys are the public contract with every tool that reads the engine: renaming one
// silently orphans every existing project.
func TestLabelKeysAreStable(t *testing.T) {
	for constant, want := range map[string]string{
		LabelProject:         "ai.egzo.project",
		LabelService:         "ai.egzo.service",
		LabelKind:            "ai.egzo.kind",
		LabelConfigHash:      "ai.egzo.config-hash",
		LabelSpecVersion:     "ai.egzo.spec-version",
		LabelProjectDir:      "ai.egzo.project-dir",
		LabelCreatedBy:       "ai.egzo.created-by",
		LabelAgentHarness:    "ai.egzo.agent.harness",
		LabelAgentWorkspaces: "ai.egzo.agent.workspaces",
		LabelInstance:        "ai.egzo.instance",
		LabelActor:           "ai.egzo.actor",
		LabelTemplateHash:    "ai.egzo.template-hash",
	} {
		if constant != want {
			t.Errorf("label key = %q, want %q", constant, want)
		}
		if !strings.HasPrefix(constant, LabelPrefix) {
			t.Errorf("label key %q lacks the %q prefix", constant, LabelPrefix)
		}
	}
}
