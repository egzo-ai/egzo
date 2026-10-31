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
	Hub        *Hub                 `yaml:"hub"`
	SecretKey  string               `yaml:"secret_key"`
	Vaults     map[string]Vault     `yaml:"vaults"`
	Proxy      Proxy                `yaml:"proxy"`
	Egress     map[string]Profile   `yaml:"egress"`
	Workspaces map[string]Workspace `yaml:"workspaces"`
	Agents     map[string]Agent     `yaml:"agents"`
	Control    Control              `yaml:"control"`
}

// Hub is the hub a project links to: its address, or a mapping that also names the CA of its certificate.
type Hub struct {
	URL string `yaml:"url"`
	CA  string `yaml:"ca,omitempty"`
}

// UnmarshalYAML accepts an address or a mapping.
func (h *Hub) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		h.URL = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: hub is an address like https://hub.example.org, or an object with url and ca", node.Line)
	}
	type plain Hub
	return node.Decode((*plain)(h))
}

// Vault is one secret store: a backend. Its secrets are declared where they are used, by their reference
// <vault>/<name>; nothing says where a secret is, the backend knows.
type Vault struct {
	Backend string `yaml:"backend"`
}

// Proxy is the infrastructure of the egress sidecar. Policy lives in Egress.
type Proxy struct {
	Image    string    `yaml:"image,omitempty"`
	Upstream *Upstream `yaml:"upstream,omitempty"`
}

// Upstream is what the proxy trusts and may dial on the way out, for services inside a company network: CA, a file of
// PEM certificates added to the system roots for the TLS the proxy makes upstream, and PrivateHosts, the exact host
// names it may reach at a private address. Reaching one still takes a profile that allows it.
type Upstream struct {
	CA           string   `yaml:"ca,omitempty"`
	PrivateHosts []string `yaml:"private_hosts,omitempty"`
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

// ServiceEntry is either a secret reference for a service that already resolves (Ref), or an object (Def): with
// hosts, a definition; without, a binding of a credential to a service that already resolves.
type ServiceEntry struct {
	Ref string
	Def *ServiceDef
}

type ServiceDef struct {
	Hosts      []string    `yaml:"hosts"`
	Inject     *Inject     `yaml:"inject,omitempty"`
	Credential *Credential `yaml:"credential,omitempty"`
	// Paths limits the injection to requests whose path starts with one of these prefixes (whole segments).
	// Nil injects on every path.
	Paths []string `yaml:"paths,omitempty"`
	// Placeholder is the environment variable that holds the stand-in for the secret in the agent.
	Placeholder string `yaml:"placeholder,omitempty"`
	Inspect     bool   `yaml:"inspect,omitempty"`
}

// The methods of a credential: how the proxy gets what it puts into requests.
const (
	MethodSecret = "secret" // a secret read from a vault at `up`
	MethodOAuth  = "oauth"  // the tokens of a sign-in, captured at the token URL and renewed by the proxy
)

// Credential is how a service gets what the proxy puts into its requests. A string is short for a secret reference.
type Credential struct {
	Method string `yaml:"method,omitempty"`
	// Secret is the <vault>/<secret> of a secret credential.
	Secret string `yaml:"secret,omitempty"`
	// TokenURL is where an OAuth client exchanges a code and refreshes its tokens; RevokeURL where it revokes them.
	TokenURL  string `yaml:"token_url,omitempty"`
	RevokeURL string `yaml:"revoke_url,omitempty"`
	// short is a credential written as a bare string: an empty one is an invalid reference, not a missing secret.
	short bool
}

// UnmarshalYAML accepts a secret reference or a mapping.
func (c *Credential) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		*c = Credential{Method: MethodSecret, Secret: node.Value, short: true}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a credential is a secret reference like vault/SECRET, or an object with a method", node.Line)
	}
	type plain Credential
	return node.Decode((*plain)(c))
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
	Build       *Build            `yaml:"build,omitempty"`
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

// Build is an image `up` builds from a Dockerfile, in Compose's terms. The context is relative to the project
// file, the Dockerfile to the context.
type Build struct {
	Context    string            `yaml:"context,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty"`
	Args       map[string]string `yaml:"args,omitempty"`
	Target     string            `yaml:"target,omitempty"`
}

// UnmarshalYAML accepts a string (the context, Compose's short form) or a mapping.
func (b *Build) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return fmt.Errorf("line %d: build must be a context directory like ./agent, or a mapping", node.Line)
		}
		*b = Build{Context: node.Value}
		return nil
	case yaml.MappingNode:
		type plain Build
		return node.Decode((*plain)(b))
	}
	return fmt.Errorf("line %d: build must be a context directory like ./agent, or a mapping", node.Line)
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
