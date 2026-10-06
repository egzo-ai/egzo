// Package engine talks to Docker or Podman through the Docker Engine API. The engine is the
// source of truth: egzo keeps no state of its own, only labels on what it creates.
package engine

import "github.com/egzo-ai/egzo/internal/version"

// Label keys follow the DNS convention under egzo.ai. They are the public contract with every
// tool that reads the engine, so they are versioned by SpecVersion.
const (
	LabelPrefix      = "ai.egzo."
	LabelProject     = LabelPrefix + "project"
	LabelService     = LabelPrefix + "service"
	LabelKind        = LabelPrefix + "kind"
	LabelConfigHash  = LabelPrefix + "config-hash"
	LabelSpecVersion = LabelPrefix + "spec-version"
	LabelProjectDir  = LabelPrefix + "project-dir"
	LabelCreatedBy   = LabelPrefix + "created-by"

	// Labels of an instance (an agent spawned from a template). LabelService holds the template's key.
	LabelInstance     = LabelPrefix + "instance"
	LabelActor        = LabelPrefix + "actor"
	LabelTemplateHash = LabelPrefix + "template-hash"

	// Semantic labels that let a tool show an agent card without parsing a spec.
	LabelAgentHarness    = LabelPrefix + "agent.harness"
	LabelAgentWorkspaces = LabelPrefix + "agent.workspaces"

	// SpecVersion is the version of the label contract.
	SpecVersion = "1"
)

// Identity says which project and service a resource belongs to.
type Identity struct {
	Project    string
	Service    string
	Kind       string
	ProjectDir string
	ConfigHash string
	// Instance, Actor and TemplateHash are set on what belongs to an instance: its container, network
	// and home volume.
	Instance     string
	Actor        string
	TemplateHash string
	// Extra holds additional ai.egzo.* labels, such as the agent's harness.
	Extra map[string]string
}

// Labels returns the labels every resource egzo creates carries.
func (i Identity) Labels() map[string]string {
	labels := map[string]string{
		LabelProject:     i.Project,
		LabelService:     i.Service,
		LabelKind:        i.Kind,
		LabelConfigHash:  i.ConfigHash,
		LabelSpecVersion: SpecVersion,
		LabelProjectDir:  i.ProjectDir,
		LabelCreatedBy:   "egzo/" + version.Version,
	}
	if i.Instance != "" {
		labels[LabelInstance] = i.Instance
		labels[LabelActor] = i.Actor
		labels[LabelTemplateHash] = i.TemplateHash
	}
	for key, value := range i.Extra {
		labels[key] = value
	}
	return labels
}
