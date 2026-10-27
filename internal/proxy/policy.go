// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
)

// Policy is what the CLI pushes to the proxy through its operator API: the egress profiles, with the
// secret values their services inject. It only ever travels through exec stdin and lives in the proxy's
// memory. Who may use which profile is a separate matter (Binding): an instance is bound to a profile
// when it is spawned, by name and token alone, so spawning never needs a secret.
type Policy struct {
	// Hash identifies the policy, secret values included, so the CLI can tell whether the proxy
	// already has it without ever reading a secret back.
	Hash string `json:"hash"`
	// Project and SecretKey derive the keys that seal what the proxy keeps at rest (see package seal).
	Project   string             `json:"project"`
	SecretKey string             `json:"secret_key"`
	Profiles  map[string]Profile `json:"profiles"`
	Upstream  *Upstream          `json:"upstream,omitempty"`
}

// Profile is one egress profile: what an agent bound to it may reach, and what is injected on the way.
type Profile struct {
	Allow    []string    `json:"allow"`
	Services []Service   `json:"services"`
	SSH      []SSHServer `json:"ssh,omitempty"`
}

// SSHServer is a server an agent of the profile may ssh to under Name. Key is the private key itself (it travels
// with the policy, like a service's secret, and never reaches an agent); HostKey is the pinned key, a SHA256
// fingerprint or a public key line.
type SSHServer struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	Key     string `json:"key"`
	HostKey string `json:"host_key"`
}

// Binding ties an agent's proxy credentials to the profile it runs under.
type Binding struct {
	// Instance is the instance's id: the name of an agent can come back, the id never does. Every audit event of the agent has it.
	Instance string `json:"instance"`
	Token    string `json:"token"`
	Profile  string `json:"profile"`
	// SSHKey is the agent's ssh identity: an ed25519 public key line. It is an identity, not a secret; the agent's private
	// half signs in to the proxy's ssh listener. Empty means the agent has no ssh access.
	SSHKey string `json:"ssh_key,omitempty"`
}

// Service is a set of hosts and, when the service injects a credential, how.
type Service struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
	// Paths, when set, limits the injection to requests whose path lies under one of these prefixes.
	Paths  []string `json:"paths,omitempty"`
	Header string   `json:"header,omitempty"`
	Value  string   `json:"value,omitempty"` // contains {secret}; empty means the raw secret
	// BasicUser, when set, makes the header HTTP Basic authentication: the user, and Secret as the password.
	BasicUser string `json:"basic_user,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Inspect   bool   `json:"inspect,omitempty"`
	// Placeholder is the text the agent types instead of the secret (not the name of the variable that
	// holds it). The proxy swaps it for Secret on requests to Hosts. A placeholder service has no Header.
	Placeholder string `json:"placeholder,omitempty"`
	// OAuth makes the service's credential a sign-in: the proxy swaps the stand-in bearer token a client sends on
	// Hosts (and Paths) for the real one, and answers at the token and revoke URLs. An OAuth service has no Header.
	OAuth *OAuth `json:"oauth,omitempty"`
}

// Injection is a header the proxy sets on requests to an intercepted host: on every request, or with Paths only
// on requests under one of them. Service names it in the audit. For an OAuth service it is the swap of a stand-in bearer
// token (OAuthService).
type Injection struct {
	Service      string
	Header       string
	Value        string
	Paths        []string
	OAuthService string
	OAuth        OAuth
}

// Endpoint is the token and revoke URLs of an OAuth service, on a host it intercepts: the proxy answers there.
type Endpoint struct {
	Service string
	OAuth   OAuth
	Token   string // the path of the token URL on this host, or ""
	Revoke  string // the path of the revoke URL on this host, or ""
}

// Covers says whether a request to escaped (the path as it goes on the wire) gets the header. Under one of the prefixes,
// a path that may mean another one to the server (config.PathProblem) is refused, with the reason: the caller answers
// it with an error.
func (i *Injection) Covers(escaped string) (inject bool, refuse string) {
	if i.Paths == nil {
		return true, ""
	}
	path, err := url.PathUnescape(escaped)
	if err != nil {
		return false, config.PathProblem(escaped) // a '%' that is not an escape
	}
	for _, prefix := range i.Paths {
		if config.PathUnder(prefix, path) {
			if reason := config.PathProblem(escaped); reason != "" {
				return false, reason
			}
			return true, ""
		}
	}
	return false, ""
}

// Substitution is a placeholder the proxy replaces with a secret value on requests to an intercepted host.
type Substitution struct {
	Service     string // names it in the audit
	Placeholder string
	Secret      string
}

// Decision is what the policy says about a connection to a host.
type Decision struct {
	Allowed bool
	Service string     // the service that covers the host, if any
	Inject  *Injection // set when the host must be intercepted to inject a credential
	Inspect bool
	// Substitutions are the placeholders to swap on this host: it is intercepted, and its request
	// bodies are bounded and scanned. A profile may have several, from several services.
	Substitutions []Substitution
	// Endpoints are the OAuth token and revoke URLs on this host: it is intercepted, and the proxy answers them.
	Endpoints []Endpoint
	Reason    string
}

// Intercepted reports whether the proxy terminates TLS for the host.
func (d Decision) Intercepted() bool {
	return d.Inject != nil || d.Inspect || len(d.Substitutions) > 0 || len(d.Endpoints) > 0
}

// endpointOn is the endpoint of an OAuth service on host, when its token or revoke URL is there. The host of the token
// URL is reached through the service: nothing else needs to allow it, and nothing there gets a token.
func endpointOn(service Service, host string) (Endpoint, bool) {
	endpoint := Endpoint{Service: service.Name, OAuth: *service.OAuth}
	for _, raw := range []string{service.OAuth.TokenURL, service.OAuth.RevokeURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), host) {
			continue
		}
		if raw == service.OAuth.TokenURL {
			endpoint.Token = u.EscapedPath()
		} else {
			endpoint.Revoke = u.EscapedPath()
		}
	}
	return endpoint, endpoint.Token != "" || endpoint.Revoke != ""
}

// Decide applies an agent's profile to a host: a service covering the host wins (and may inject),
// then the profile's allow list. Anything else is denied.
func (a *Profile) Decide(host string) Decision {
	host = strings.ToLower(host)
	var decision Decision
	named := false // the decision's service lists the host exactly
	for _, service := range a.Services {
		if service.OAuth != nil {
			if endpoint, ok := endpointOn(service, host); ok {
				if !decision.Allowed {
					decision = Decision{Allowed: true, Service: service.Name}
				}
				decision.Endpoints = append(decision.Endpoints, endpoint)
			}
		}
		covers, exact := false, false
		for _, pattern := range service.Hosts {
			pattern = strings.ToLower(pattern)
			if config.MatchHost(pattern, host) {
				covers = true
				exact = exact || pattern == host
			}
		}
		if !covers {
			continue
		}
		// The first service covering the host names it, unless a later one lists the host exactly
		// (the audit names the most specific service); every covering service may add its placeholder.
		if !decision.Allowed {
			decision = Decision{Allowed: true, Service: service.Name}
			named = exact
		} else if exact && !named {
			decision.Service = service.Name
			named = true
		}
		decision.Inspect = decision.Inspect || service.Inspect
		if service.Header != "" && decision.Inject == nil {
			value := service.Secret
			switch {
			case service.BasicUser != "":
				value = "Basic " + base64.StdEncoding.EncodeToString([]byte(service.BasicUser+":"+service.Secret))
			case service.Value != "":
				value = strings.ReplaceAll(service.Value, "{secret}", service.Secret)
			}
			decision.Inject = &Injection{Service: service.Name, Header: service.Header, Value: value, Paths: service.Paths}
		}
		if service.OAuth != nil && decision.Inject == nil {
			decision.Inject = &Injection{Service: service.Name, Paths: service.Paths, OAuthService: service.Name, OAuth: *service.OAuth}
		}
		if service.Placeholder != "" {
			decision.Substitutions = append(decision.Substitutions, Substitution{service.Name, service.Placeholder, service.Secret})
		}
	}
	if decision.Allowed {
		return decision
	}
	for _, pattern := range a.Allow {
		if config.MatchHost(strings.ToLower(pattern), host) {
			return Decision{Allowed: true}
		}
	}
	return Decision{Reason: "not allowed by the agent's egress profile"}
}
