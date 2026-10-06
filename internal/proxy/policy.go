package proxy

import (
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
	Hash     string             `json:"hash"`
	Profiles map[string]Profile `json:"profiles"`
}

// Profile is one egress profile: what an agent bound to it may reach, and what is injected on the way.
type Profile struct {
	Allow    []string  `json:"allow"`
	Services []Service `json:"services"`
}

// Binding ties an agent's proxy credentials to the profile it runs under.
type Binding struct {
	Token   string `json:"token"`
	Profile string `json:"profile"`
}

// Service is a set of hosts and, when the service injects a credential, how.
type Service struct {
	Name    string   `json:"name"`
	Hosts   []string `json:"hosts"`
	Header  string   `json:"header,omitempty"`
	Value   string   `json:"value,omitempty"` // contains {secret}; empty means the raw secret
	Secret  string   `json:"secret,omitempty"`
	Inspect bool     `json:"inspect,omitempty"`
}

// Injection is a header the proxy sets on requests to an intercepted host.
type Injection struct {
	Header string
	Value  string
}

// Decision is what the policy says about a connection to a host.
type Decision struct {
	Allowed bool
	Service string     // the service that covers the host, if any
	Inject  *Injection // set when the host must be intercepted to inject a credential
	Inspect bool
	Reason  string
}

// Decide applies an agent's profile to a host: a service covering the host wins (and may inject),
// then the profile's allow list. Anything else is denied.
func (a *Profile) Decide(host string) Decision {
	host = strings.ToLower(host)
	for _, service := range a.Services {
		for _, pattern := range service.Hosts {
			if !config.MatchHost(strings.ToLower(pattern), host) {
				continue
			}
			decision := Decision{Allowed: true, Service: service.Name, Inspect: service.Inspect}
			if service.Header != "" {
				value := service.Secret
				if service.Value != "" {
					value = strings.ReplaceAll(service.Value, "{secret}", service.Secret)
				}
				decision.Inject = &Injection{Header: service.Header, Value: value}
			}
			return decision
		}
	}
	for _, pattern := range a.Allow {
		if config.MatchHost(strings.ToLower(pattern), host) {
			return Decision{Allowed: true}
		}
	}
	return Decision{Reason: "not allowed by the agent's egress profile"}
}
