package proxy

import (
	"crypto/subtle"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
)

// Policy is what the CLI pushes to the proxy through its operator API. It carries secret values,
// so it only ever travels through exec stdin and lives in the proxy's memory.
type Policy struct {
	// Hash identifies the policy, secret values included, so the CLI can tell whether the proxy
	// already has it without ever reading a secret back.
	Hash   string                 `json:"hash"`
	Agents map[string]AgentPolicy `json:"agents"`
}

// AgentPolicy is the egress profile of one agent, with the token that identifies it.
type AgentPolicy struct {
	Token    string    `json:"token"`
	Allow    []string  `json:"allow"`
	Services []Service `json:"services"`
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

// Authenticate finds the agent a proxy credential belongs to.
func (p *Policy) Authenticate(agent, token string) (*AgentPolicy, bool) {
	policy, ok := p.Agents[agent]
	if !ok || policy.Token == "" || subtle.ConstantTimeCompare([]byte(policy.Token), []byte(token)) != 1 {
		return nil, false
	}
	return &policy, true
}

// Decide applies an agent's profile to a host: a service covering the host wins (and may inject),
// then the profile's allow list. Anything else is denied.
func (a *AgentPolicy) Decide(host string) Decision {
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
