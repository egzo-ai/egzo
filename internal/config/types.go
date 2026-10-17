// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package config loads, validates and resolves egzo.yaml.
//
// The raw types mirror the file. Resolve turns them into the fully resolved view that
// `egzo project check --resolved` prints and the rest of egzo works from.
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

// Vault is one secret store: a backend and the names of the secrets read from it. Nothing says where a
// secret is; the backend knows.
type Vault struct {
	Backend string   `yaml:"backend"`
	Secrets []string `yaml:"secrets"`
}

// Proxy is the infrastructure of the egress sidecar. Policy lives in Egress.
type Proxy struct {
	Image string `yaml:"image,omitempty"`
}

// Control is the control sidecar's section. It has no settings yet; the key is accepted so a file can
// carry the section the design shows, and a setting that does nothing is rejected rather than ignored.
type Control struct{}

// Profile is a named egress profile.
type Profile struct {
	Extend   string                  `yaml:"extend,omitempty"`
	Allow    []string                `yaml:"allow,omitempty"`
	Services map[string]ServiceEntry `yaml:"services,omitempty"`
	SSH      map[string]SSHServer    `yaml:"ssh,omitempty"`
}

// SSHServer is a server an agent of the profile may ssh to. The proxy holds the key and re-originates the connection;
// the agent only types the name. HostKey is required: there is no trust on first use.
type SSHServer struct {
	Host    string  `yaml:"host"`
	Port    SSHPort `yaml:"port,omitempty"`
	User    string  `yaml:"user"`
	Key     string  `yaml:"key"`
	HostKey string  `yaml:"host_key"`
}

// SSHPort is a port number; zero is "not given" (22).
type SSHPort int

func (p *SSHPort) UnmarshalYAML(node *yaml.Node) error {
	var n int
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" || node.Decode(&n) != nil || n < 1 || n > 65535 {
		return fmt.Errorf("line %d: ssh port must be a number from 1 to 65535", node.Line)
	}
	*p = SSHPort(n)
	return nil
}

// ServiceEntry is either a secret reference for a service that already resolves (Ref), or
// a service definition (Def).
type ServiceEntry struct {
	Ref string
	Def *ServiceDef
}

type ServiceDef struct {
	Hosts  []string `yaml:"hosts"`
	Inject *Inject  `yaml:"inject,omitempty"`
	Secret string   `yaml:"secret,omitempty"`
	// Placeholder is the environment variable that holds the stand-in for the secret in the agent.
	Placeholder string `yaml:"placeholder,omitempty"`
	Inspect     bool   `yaml:"inspect,omitempty"`
}

// Inject says how a service's secret is put into a request header. With neither Value nor BasicUser the
// header is the raw secret; Value is a template around {secret}; BasicUser sends HTTP Basic authentication
// (the user, and the secret as its password).
type Inject struct {
	Header    string `yaml:"header"`
	Value     string `yaml:"value,omitempty"`
	BasicUser string `yaml:"basic_user,omitempty"`
}

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
		var def ServiceDef
		if err := node.Decode(&def); err != nil {
			return err
		}
		e.Def = &def
		return nil
	}
	return fmt.Errorf("line %d: a service must be a secret reference like vault/SECRET, or a definition", node.Line)
}

// Workspace declares a workspace of the project. Without git it is an empty directory kept in an engine volume;
// with git, a base clone and one worktree per instance, each in a volume. Nothing of it is on the host.
type Workspace struct {
	Git *Git `yaml:"git,omitempty"`
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
	Resources   Resources         `yaml:"resources,omitempty"`
	Permissions string            `yaml:"permissions,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	Inject      *AgentInject      `yaml:"inject,omitempty"`
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
	// Pids caps the processes and threads of the agent; egzo sets a generous default when it is left out.
	Pids int64 `yaml:"pids,omitempty"`
}
