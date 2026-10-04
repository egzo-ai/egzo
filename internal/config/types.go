// Package config loads, validates and resolves egzo.yaml.
//
// The raw types mirror the file. Resolve turns them into the fully resolved view that
// `egzo config` prints and the rest of egzo works from.
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// File is egzo.yaml as written.
type File struct {
	Version    int                  `yaml:"version"`
	Name       *string              `yaml:"name"`
	Vaults     map[string]Vault     `yaml:"vaults"`
	Proxy      Proxy                `yaml:"proxy"`
	Egress     map[string]Profile   `yaml:"egress"`
	Workspaces map[string]Workspace `yaml:"workspaces"`
	Agents     map[string]Agent     `yaml:"agents"`
	Control    Control              `yaml:"control"`
}

type Vault struct {
	Backend string                  `yaml:"backend"`
	Secrets map[string]SecretSource `yaml:"secrets"`
}

type SecretSource struct {
	From string `yaml:"from"`
}

// Proxy is the infrastructure of the egress sidecar. Policy lives in Egress.
type Proxy struct {
	Image string `yaml:"image,omitempty"`
	Audit *bool  `yaml:"audit,omitempty"`
}

type Control struct {
	Tools []string `yaml:"tools,omitempty"`
}

// Profile is a named egress profile.
type Profile struct {
	Extend   string                  `yaml:"extend,omitempty"`
	Allow    []string                `yaml:"allow,omitempty"`
	Services map[string]ServiceEntry `yaml:"services,omitempty"`
}

// ServiceEntry is either a secret reference for a service that already resolves (Ref), or
// a service definition (Def).
type ServiceEntry struct {
	Ref string
	Def *ServiceDef
}

type ServiceDef struct {
	Hosts   []string `yaml:"hosts"`
	Inject  *Inject  `yaml:"inject,omitempty"`
	Secret  string   `yaml:"secret,omitempty"`
	Inspect bool     `yaml:"inspect,omitempty"`
}

type Inject struct {
	Header string `yaml:"header"`
	Value  string `yaml:"value,omitempty"`
}

var serviceDefKeys = map[string]bool{"hosts": true, "inject": true, "secret": true, "inspect": true}

// UnmarshalYAML accepts a string (secret reference) or a mapping (definition). Null and any
// other shape are rejected.
func (e *ServiceEntry) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return fmt.Errorf("line %d: a service needs a secret reference or a definition, not an empty value", node.Line)
		}
		if node.Tag != "!!str" {
			return fmt.Errorf("line %d: a service must be a secret reference like vault/SECRET, or a definition", node.Line)
		}
		e.Ref = node.Value
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if key := node.Content[i].Value; !serviceDefKeys[key] {
				return fmt.Errorf("line %d: unknown key %q in service definition (allowed: hosts, inject, secret, inspect)", node.Content[i].Line, key)
			}
		}
		var def ServiceDef
		if err := node.Decode(&def); err != nil {
			return err
		}
		e.Def = &def
		return nil
	}
	return fmt.Errorf("line %d: a service must be a secret reference like vault/SECRET, or a definition", node.Line)
}

// Workspace declares a workspace. Without a source it is a shared system volume.
type Workspace struct {
	Git  *Git   `yaml:"git,omitempty"`
	Mode string `yaml:"mode,omitempty"`
	Path string `yaml:"path,omitempty"`
}

type Git struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
}

type Agent struct {
	Harness     string            `yaml:"harness"`
	Image       string            `yaml:"image,omitempty"`
	Workspaces  []string          `yaml:"workspaces,omitempty"`
	Workdir     string            `yaml:"workdir,omitempty"`
	Egress      string            `yaml:"egress,omitempty"`
	Model       string            `yaml:"model,omitempty"`
	Prompt      string            `yaml:"prompt,omitempty"`
	Tools       []string          `yaml:"tools,omitempty"`
	Resources   Resources         `yaml:"resources,omitempty"`
	Runtime     string            `yaml:"runtime,omitempty"`
	Permissions string            `yaml:"permissions,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	Inject      *AgentInject          `yaml:"inject,omitempty"`
}

// AgentInject tunes how queued messages are typed into the agent's terminal.
type AgentInject struct {
	HumanQuiet string `yaml:"human_quiet,omitempty"`
	AckTimeout string `yaml:"ack_timeout,omitempty"`
	IdleSignal string `yaml:"idle_signal,omitempty"`
	Quiescence string `yaml:"quiescence,omitempty"`
}

type Resources struct {
	CPUs   float64 `yaml:"cpus,omitempty"`
	Memory string  `yaml:"memory,omitempty"`
}
