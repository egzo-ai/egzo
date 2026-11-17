// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egzo-ai/egzo/internal/harness"
)

func TestACustomAgentsInstructionsAreWhatTheOtherHarnessesAreTold(t *testing.T) {
	dir := t.TempDir()
	options := harness.Options{Agent: "linter", Template: "linter", Harness: "custom", Starter: "operator"}
	path, err := writeInstructions(dir, options)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "egzo", "instructions.md") {
		t.Errorf("written to %s", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(harness.InstructionsFor(options)) {
		t.Errorf("a custom agent is told something else:\n%s", written)
	}
}
