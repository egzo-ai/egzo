// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const (
	sshHostKeyFile = "ssh_host_ed25519_key"
	sshHostPubFile = "ssh_host_key.pub"
)

// LoadOrCreateSSHHostKey returns the key the proxy's ssh listener identifies itself with, made on first use and kept in
// the private volume. Agents trust it through the public half, which Publish writes into the shared volume.
func LoadOrCreateSSHHostKey(dir string) (ssh.Signer, error) {
	path := filepath.Join(dir, sshHostKeyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("ssh host key: %w", err)
		}
		return signer, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(private, "egzo proxy")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeAtomic(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(private)
}

// PublishSSHHostKey writes the public half where agents read it, as a line they can put in known_hosts after a host name.
func PublishSSHHostKey(signer ssh.Signer, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, sshHostPubFile), ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644)
}
