// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/version"
)

// Snapshot is the resolved project as it was applied: what the control sidecar keeps on its volume,
// keyed by hash, so any tool with engine access can show what a project runs under. It holds
// references to secrets, never their values, and the container config hashes so a stale snapshot
// is detectable against the labels.
type Snapshot struct {
	CreatedBy    string            `yaml:"created-by"`
	SpecVersion  string            `yaml:"spec-version"`
	ConfigHashes map[string]string `yaml:"config-hashes"`
	Config       *config.Resolved  `yaml:"config"`
}

func newSnapshot(project *config.Resolved, desired Desired) ([]byte, string, error) {
	snapshot := Snapshot{
		CreatedBy:    "egzo/" + version.Version,
		SpecVersion:  engine.SpecVersion,
		ConfigHashes: map[string]string{},
		Config:       project,
	}
	for _, spec := range desired.Containers {
		snapshot.ConfigHashes[spec.Name] = spec.Identity.ConfigHash
	}
	data, err := yaml.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// pushSnapshot stores the snapshot on the control volume unless it is already there.
func pushSnapshot(ctx context.Context, c *engine.Client, project *config.Resolved, desired Desired, fresh bool, out io.Writer) (bool, error) {
	data, hash, err := newSnapshot(project, desired)
	if err != nil {
		return false, err
	}
	// A control volume this run just created holds no snapshot yet.
	if !fresh {
		probe, err := c.Exec(ctx, desired.Control, []string{"/egzo", "control", "request", "GET", "/specs/" + hash}, nil)
		if err != nil {
			return false, fmt.Errorf("check spec snapshot: %w", err)
		}
		if probe.ExitCode == 0 {
			return false, nil
		}
	}
	result, err := c.Exec(ctx, desired.Control, []string{"/egzo", "control", "request", "PUT", "/specs/" + hash}, bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("store spec snapshot: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("store spec snapshot: %s", strings.TrimSpace(string(result.Stderr)))
	}
	fmt.Fprintln(out, "store spec snapshot "+hash[:12])
	return true, nil
}
