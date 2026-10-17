// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/ssh"
)

// AgentCADir is where an agent finds what the proxy publishes for it: the CA, and the ssh client files.
const AgentCADir = "/etc/egzo/ca"

// SSHListenPort is the port of the proxy's ssh listener, as the agents reach it.
const SSHListenPort = "2222"

// The ssh identity of an instance is derived from its token (the one that already identifies it to the proxy and to
// control), so there is nothing more to store: the host side derives the public half to bind, the instance derives the
// private half to sign in with. It identifies an instance to the proxy; it is not a credential for anything else.
func identityKey(token string) (ed25519.PrivateKey, error) {
	if token == "" {
		return nil, fmt.Errorf("an ssh identity needs the instance's token")
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(token), nil, []byte("egzo ssh identity v1")), seed); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// IdentityPublicKey is the line the proxy binds for an instance: its public key.
func IdentityPublicKey(token string) (string, error) {
	private, err := identityKey(token)
	if err != nil {
		return "", err
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

// IdentityPrivateKey is the OpenSSH private key file an instance signs in with.
func IdentityPrivateKey(token string) ([]byte, error) {
	private, err := identityKey(token)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(private, "egzo instance identity")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// IdentityFile is the name, in ~/.ssh, of the identity an instance signs in to the proxy with.
const IdentityFile = "egzo_ed25519"

// PublishSSHClientFiles writes what an agent's ssh needs into the shared volume: a ssh_config with one Host per server
// name of the policy (every one is the proxy's listener, told apart by the user), and a known_hosts that holds the
// proxy's host key. Both are the same for every agent: which servers an agent may use is decided by the proxy.
func PublishSSHClientFiles(dir string, hostKey ssh.PublicKey, policy *Policy) error {
	names := map[string]bool{}
	if policy != nil {
		for _, profile := range policy.Profiles {
			for _, server := range profile.SSH {
				names[server.Name] = true
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	var config strings.Builder
	config.WriteString("# Written by the egzo proxy. Which of these servers an agent may use is decided by the proxy, not by this file.\n")
	for _, name := range sorted {
		fmt.Fprintf(&config, `Host %[1]s
    HostName proxy
    Port %[2]s
    User %[1]s
    IdentityFile ~/.ssh/%[3]s
    IdentitiesOnly yes
    PreferredAuthentications publickey
    GlobalKnownHostsFile %[4]s/ssh_known_hosts
    UserKnownHostsFile /dev/null
    HostKeyAlias egzo-proxy
    StrictHostKeyChecking yes
    ForwardAgent no
    ForwardX11 no
    ServerAliveInterval 30
`, name, SSHListenPort, IdentityFile, AgentCADir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	known := "egzo-proxy " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))) + "\n"
	if err := writeAtomic(filepath.Join(dir, "ssh_known_hosts"), []byte(known), 0o644); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "ssh_config"), []byte(config.String()), 0o644)
}
