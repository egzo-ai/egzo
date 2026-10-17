// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/proxy"
)

func newSSHSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "ssh-setup",
		Short:  "Give this agent's ssh the identity and the servers its egress profile names",
		Hidden: true,
		Long: "Write this instance's ssh identity (derived from EGZO_TOKEN) to ~/.ssh and put the servers the proxy\n" +
			"publishes for the egress profiles into ~/.ssh/config, so that `ssh <server>` reaches the proxy, which holds the real key. `egzo agent run` does\n" +
			"this when it starts; run it by hand in an image whose entrypoint is not `egzo agent run`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home := os.Getenv("HOME")
			if home == "" {
				return errors.New("HOME is not set")
			}
			return setupSSH(home, os.Getenv("EGZO_TOKEN"), filepath.Join(proxy.AgentCADir, "ssh_config"))
		},
	}
}

// setupSSH writes the instance's identity and the servers the proxy published into ~/.ssh/config. Without a token (not an egzo
// agent) there is nothing to do.
func setupSSH(home, token, publishedConfig string) error {
	if token == "" {
		return nil
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := proxy.IdentityPrivateKey(token)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, proxy.IdentityFile), key, 0o600); err != nil {
		return err
	}
	published, err := os.ReadFile(publishedConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	path := filepath.Join(dir, "config")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFileAtomic(path, withManagedBlock(string(existing), string(published)), 0o600)
}

const (
	managedBegin = "# BEGIN egzo (written by `egzo agent run`; replaced at each start)\n"
	managedEnd   = "# END egzo\n"
)

// withManagedBlock puts the servers the proxy published at the top of an ssh config, in a block that is replaced each time (a
// block after a Host line of the person's own would only apply to that host). Copied rather than Include'd: ssh refuses an
// included file that is not owned by the user or root, and the proxy that writes it is neither.
func withManagedBlock(existing, published string) []byte {
	if start := strings.Index(existing, managedBegin); start >= 0 {
		if end := strings.Index(existing[start:], managedEnd); end >= 0 {
			existing = existing[:start] + existing[start+end+len(managedEnd):]
		}
	}
	if strings.TrimSpace(published) == "" {
		return []byte(existing)
	}
	return []byte(managedBegin + strings.TrimRight(published, "\n") + "\n" + managedEnd + existing)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
