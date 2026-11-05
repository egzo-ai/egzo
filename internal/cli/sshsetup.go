// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// sshFollowInterval is how often a running agent looks at the servers the proxy publishes. `up` takes longer than that.
const sshFollowInterval = 10 * time.Second

// sshFollower keeps the agent's ssh config in step with the servers the proxy publishes again whenever `up` loads a policy.
// A look is one stat of a file of the shared volume (the proxy replaces it whole, so a change moves its time or size):
// file-change notifications do not cross containers under gVisor or a VM-backed engine. It looks every so often, and once
// more right before a line is typed into the agent's terminal, so an agent told its access changed finds its new servers.
type sshFollower struct {
	home, token, published string
	mu                     sync.Mutex
	last                   string // what the last look saw; nothing yet, so the first look writes the config once more
}

// Check rewrites the config when the published servers changed since the last look. Errors are said once per change, as
// setupSSH's at the start are: ssh that cannot follow is not worth stopping the agent for.
func (f *sshFollower) Check() {
	if f.token == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := "gone"
	if info, err := os.Stat(f.published); err == nil {
		seen = fmt.Sprintf("%d %d", info.ModTime().UnixNano(), info.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	if seen == f.last {
		return
	}
	f.last = seen
	if err := setupSSH(f.home, f.token, f.published); err != nil {
		fmt.Fprintf(os.Stderr, "egzo: could not update ssh: %v\n", err)
	}
}

// Follow looks every so often until done closes; nil follows for the life of the process.
func (f *sshFollower) Follow(every time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			f.Check()
		}
	}
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
