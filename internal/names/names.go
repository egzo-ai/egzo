// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package names holds the rules of the names egzo makes and accepts: of a project, an instance, an actor, an id, a hash and a
// host. A name becomes a container, network or volume name, a DNS name, a label, a git ref or a file on a sidecar's volume, and
// what one part of egzo writes another reads back, sometimes from an engine it does not control (the hub). One rule for each,
// here, is what keeps them agreeing: the project file, spawn, the sidecars, the labels and the hub all check with these.
package names

import (
	"net/netip"
	"regexp"
	"strings"
)

// identifier is the plain form of a name: 1 to 63 lowercase letters, digits, '-' and '_', starting with a letter or digit. 63
// is the longest a DNS label is.
var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// IdentifierRule says what an identifier is, for messages.
const IdentifierRule = "1 to 63 lowercase letters, digits, '-' and '_', starting with a letter or digit"

// Identifier reports whether s is a plain identifier: what a project, a template, an instance, a workspace or a profile is called.
func Identifier(s string) bool { return identifier.MatchString(s) }

// Project reports whether s can be the name of a project.
func Project(s string) bool { return Identifier(s) }

// Instance reports whether s can be the name of an instance. It is the form only: the project file and spawn refuse more (see
// config.CheckInstanceName).
func Instance(s string) bool { return Identifier(s) }

// instanceID is the form of an instance's id: a ULID in Crockford's base 32.
var instanceID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// InstanceID reports whether s is an instance id.
func InstanceID(s string) bool { return instanceID.MatchString(s) }

// hash is the form of what egzo fingerprints a definition or a snapshot with: SHA-256, in lowercase hex.
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Hash reports whether s is a hash egzo made.
func Hash(s string) bool { return hash.MatchString(s) }

// Operator is the actor of the CLI and of what it does.
const Operator = "operator"

// actorID is what follows the kind of an actor: a user's id (an email address is one), a service's or an agent's name.
var actorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// Actor reports whether s names who did something: "operator", or one of kinds and an id ("user:<id>"). A place that takes
// actors names the kinds it takes.
func Actor(s string, kinds ...string) bool {
	if s == Operator {
		return true
	}
	kind, id, found := strings.Cut(s, ":")
	if !found || !actorID.MatchString(id) {
		return false
	}
	for _, k := range kinds {
		if kind == k {
			return true
		}
	}
	return false
}

// sshServer is the name of an ssh server of a profile: the `Host` alias an agent types, so a lowercase label.
var sshServer = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// SSHServerRule says what the name of an ssh server is, for messages.
const SSHServerRule = "1 to 32 lowercase letters, digits and '-', starting with a letter, and not \"root\""

// SSHServer reports whether s can name an ssh server of a profile. "root" would read as the remote user.
func SSHServer(s string) bool { return sshServer.MatchString(s) && s != "root" }

// MaxHost is the longest a host name is.
const MaxHost = 253

// NormalizeHost is the form hosts are compared in: lowercase, without the dot that ends a fully qualified name.
func NormalizeHost(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

// HostRule says what a host is, for messages.
const HostRule = "a host name (dot-separated labels of 1 to 63 letters, digits, '-' and '_', 253 bytes at most) or an IP address"

// Host reports whether host, normalized, is a host name or an IP address: what a profile allows, an ssh server is reached at and an
// agent may ask the proxy for. Underscores are taken, as some real names have them.
func Host(host string) bool {
	if host == "" || len(host) > MaxHost {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Zone() == ""
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// HostHeader reads the host of an HTTP Host header, with any port dropped, normalized; an IPv6 literal comes back without its
// brackets, as a CONNECT target has it. Anything else than a host, or a bracketed IPv6 literal, and an optional numeric port is
// refused (false), so a header that some parser reads differently, such as "evil@example.com" or "example.com:443/x", is never
// compared. The port may be empty, as a URI's may.
func HostHeader(header string) (string, bool) {
	host, port := header, ""
	if strings.HasPrefix(header, "[") {
		inside, rest, found := strings.Cut(header[1:], "]")
		if !found || !strings.Contains(inside, ":") {
			return "", false
		}
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return "", false
		}
		host, port = inside, strings.TrimPrefix(rest, ":")
	} else {
		host, port, _ = strings.Cut(header, ":")
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	host = NormalizeHost(host)
	if !Host(host) {
		return "", false
	}
	return host, true
}
