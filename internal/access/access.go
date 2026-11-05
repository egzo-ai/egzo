// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package access is what an agent is told about what it may reach: the egress profile of its template, the hosts it allows,
// each service with how to use it, and the ssh servers. It holds no secret and no reference to one. Core builds it from the
// template at `up` and spawn, the control sidecar keeps it per agent and serves it (`GET /v1/access`, the `access` tool), and
// when `up` changes it for a running agent, the agent is told what changed (Changes).
package access

import (
	"fmt"
	"slices"
	"strings"
)

// Credential kinds of a service.
const (
	Injected    = "injected"    // the proxy sets the credential on requests to the service's hosts
	Placeholder = "placeholder" // the agent has a variable whose text the proxy swaps for the secret
	SignIn      = "sign-in"     // an OAuth sign-in, the project's, kept by the proxy
	None        = "none"        // no credential: the hosts are reachable (and logged when inspected)
)

// OtherHosts is what an agent is told of every host its profile does not name.
const OtherHosts = "every other host is refused by the proxy"

// Service is one service of the profile, as the agent uses it.
type Service struct {
	Name       string   `json:"name"`
	Hosts      []string `json:"hosts"`
	Paths      []string `json:"paths,omitempty"`
	Credential string   `json:"credential"`
	Variable   string   `json:"variable,omitempty"` // the placeholder variable
	Inspect    bool     `json:"inspect,omitempty"`
	How        string   `json:"how"`
}

// Access is what an agent may reach.
type Access struct {
	Profile    string    `json:"profile"`
	Allow      []string  `json:"allow"`
	OtherHosts string    `json:"other_hosts"`
	Services   []Service `json:"services"`
	SSH        []string  `json:"ssh"`
}

// Equal reports whether two descriptions say the same.
func Equal(a, b Access) bool {
	return a.Profile == b.Profile && a.OtherHosts == b.OtherHosts && slices.Equal(a.Allow, b.Allow) && slices.Equal(a.SSH, b.SSH) &&
		slices.EqualFunc(a.Services, b.Services, func(x, y Service) bool {
			return x.Name == y.Name && x.Credential == y.Credential && x.Variable == y.Variable && x.Inspect == y.Inspect && x.How == y.How &&
				slices.Equal(x.Hosts, y.Hosts) && slices.Equal(x.Paths, y.Paths)
		})
}

// How says how an agent uses a service with a credential of the kind, with a hint for the clients that are known to need one.
func How(name, credential, variable string) string {
	switch credential {
	case Injected:
		how := "send any value where the client needs a credential, or none: the proxy sets the real one on requests to these hosts"
		if name == "github" {
			how += " (gh: `GH_TOKEN=x gh ...`; git over HTTPS needs nothing)"
		}
		return how
	case Placeholder:
		return fmt.Sprintf("use $%s where the secret goes: the proxy swaps it for the secret on requests to these hosts", variable)
	case SignIn:
		return "the project's sign-in, kept by the proxy: sign in from the client when it asks, once for every agent of the project"
	}
	return "reachable, with no credential"
}

// Text is the description for a person or a harness without MCP (`egzo agent access`).
func (a Access) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "egress profile: %s\n", a.Profile)
	if len(a.Allow) > 0 {
		fmt.Fprintf(&b, "allowed hosts: %s\n", strings.Join(a.Allow, ", "))
	} else {
		b.WriteString("allowed hosts: none besides the services below\n")
	}
	for _, s := range a.Services {
		fmt.Fprintf(&b, "service %s: %s", s.Name, strings.Join(s.Hosts, ", "))
		if len(s.Paths) > 0 {
			fmt.Fprintf(&b, " (paths %s)", strings.Join(s.Paths, ", "))
		}
		fmt.Fprintf(&b, ": %s\n", s.How)
	}
	if len(a.SSH) > 0 {
		fmt.Fprintf(&b, "ssh servers: %s (`ssh <name>`)\n", strings.Join(a.SSH, ", "))
	}
	b.WriteString(a.OtherHosts + "\n")
	return b.String()
}

// Changes says, in a few words for the agent, what differs from before to after: the hosts, services and ssh servers added
// and taken away, and the profile when it is another. Empty when nothing the agent can tell changed.
func Changes(before, after Access) string {
	var parts []string
	if before.Profile != after.Profile {
		parts = append(parts, fmt.Sprintf("your egress profile is now %s", after.Profile))
	}
	add := func(what string, before, after []string) {
		if added := missing(after, before); len(added) > 0 {
			parts = append(parts, fmt.Sprintf("%s now allowed: %s", what, strings.Join(added, ", ")))
		}
		if removed := missing(before, after); len(removed) > 0 {
			parts = append(parts, fmt.Sprintf("%s no longer allowed: %s", what, strings.Join(removed, ", ")))
		}
	}
	add("hosts", before.Allow, after.Allow)
	names := func(services []Service) []string {
		out := make([]string, len(services))
		for i, s := range services {
			out[i] = s.Name + " (" + strings.Join(s.Hosts, ", ") + ")"
		}
		return out
	}
	add("services", names(before.Services), names(after.Services))
	add("ssh servers", before.SSH, after.SSH)
	if len(parts) == 0 && !Equal(before, after) {
		parts = append(parts, "how a service is used changed")
	}
	return strings.Join(parts, "; ")
}

// missing lists what is in a and not in b, in a's order.
func missing(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
