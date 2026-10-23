// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/egzo-ai/egzo/internal/names"
)

// ResolvedSSH is an ssh server of a profile after validation: the port is always set, the key is a secret reference.
type ResolvedSSH struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	User    string `yaml:"user"`
	Key     string `yaml:"key"`
	HostKey string `yaml:"host_key"`
}

const defaultSSHPort = 22

var (
	// A plain account name: never something ssh would take for an option, and nothing that needs quoting.
	sshUserName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	// The unpadded base64 SHA-256 that `ssh-keygen -lf` prints and ssh shows.
	sshFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

// applySSH validates the ssh servers a profile declares and merges them over the ones it inherited (the child wins, by name).
func (r *egressResolver) applySSH(profile string, current *ResolvedProfile, servers map[string]SSHServer) {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if resolved := r.checkSSH(profile, name, servers[name]); resolved != nil {
			current.SSH[name] = resolved
		}
	}
}

func (r *egressResolver) checkSSH(profile, name string, def SSHServer) *ResolvedSSH {
	failed := false
	fail := func(format string, args ...any) {
		r.problems.addf("egress profile %q: ssh server %q: %s", profile, name, fmt.Sprintf(format, args...))
		failed = true
	}
	if !names.SSHServer(name) {
		fail("the name is what an agent types: " + names.SSHServerRule)
	}
	host := names.NormalizeHost(def.Host)
	switch {
	case def.Host == "":
		fail("needs a host")
	case !names.Host(host):
		fail("invalid host %q: one exact host name or address, with no port, scheme or wildcard (the port is its own key)", def.Host)
	}
	switch {
	case def.User == "":
		fail("needs a user")
	case !sshUserName.MatchString(def.User):
		fail("invalid user %q: a plain account name", def.User)
	}
	if def.Key == "" {
		fail("needs a key: the <vault>/<secret> that holds the private key")
	} else if !r.checkSecret(profile, fmt.Sprintf("ssh server %q key", name), def.Key) {
		failed = true
	}
	if reason := hostKeyProblem(def.HostKey); reason != "" {
		fail("host_key %s", reason)
	}
	if failed {
		return nil
	}
	port := int(def.Port)
	if port == 0 {
		port = defaultSSHPort
	}
	return &ResolvedSSH{Host: host, Port: port, User: def.User, Key: def.Key, HostKey: strings.TrimSpace(def.HostKey)}
}

// hostKeyProblem says why a pinned host key cannot be used, or returns "". It is a SHA256 fingerprint or a public key line;
// a server nobody can verify is an error, and there is no value that means "trust what you see".
func hostKeyProblem(value string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "is required: the SHA256 fingerprint (ssh-keygen -lf) or the public key of the server, pinned (there is no trust on first use)"
	case strings.HasPrefix(value, "SHA256:"):
		if !sshFingerprint.MatchString(value) {
			return fmt.Sprintf("%q is not a SHA256 fingerprint (SHA256: and 43 base64 characters)", value)
		}
		return ""
	}
	if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(value)); err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return fmt.Sprintf("%q is neither a SHA256 fingerprint nor a public key line like \"ssh-ed25519 AAAA...\"", value)
	}
	return ""
}
