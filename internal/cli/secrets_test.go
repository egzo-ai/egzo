// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
)

func TestSecretStateForPass(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$2\" in ok) echo v;; empty) echo;; *) echo \"Error: $2 is not in the password store.\" >&2; exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "pass"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ref := func(n string) config.SecretRef { return config.SecretRef{Vault: "main", Name: n, Backend: "pass"} }
	for name, want := range map[string]string{"ok": "set", "empty": "empty", "gone": "missing (Error: gone is not in the password store.)"} {
		if got, err := secretState(ref(name), ""); err != nil || got != want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := secretState(ref("ok"), ""); err == nil || !strings.Contains(err.Error(), "pass") {
		t.Errorf("not on PATH: %v", err)
	}
}

func TestEnvWarningIsPrintedOncePerCommand(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "egzo.yaml")
	body := "name: p\nvaults:\n  main:\n    backend: env\n    secrets: [TOKEN]\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	read, write, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = write
	opts := &options{file: file}
	_, err1 := loadProject(opts)
	_, err2 := loadProject(opts)
	os.Stderr = saved
	write.Close()
	buf := make([]byte, 4096)
	n, _ := read.Read(buf)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if got := strings.Count(string(buf[:n]), EnvBackendWarning); got != 1 {
		t.Errorf("warning printed %d times: %q", got, buf[:n])
	}
}
