// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
)

// stubPass puts a `pass` script on PATH. It logs "<args>\t<PASSWORD_STORE_DIR or unset>" and serves entries from files.
func stubPass(t *testing.T) (entries, log string) {
	t.Helper()
	dir := t.TempDir()
	entries = filepath.Join(dir, "entries")
	log = filepath.Join(dir, "log")
	if err := os.MkdirAll(entries, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\t%s\\n' \"$*\" \"${PASSWORD_STORE_DIR-unset}\" >> " + log + "\n" +
		"f=" + entries + "/$(printf %s \"$2\" | tr / _)\n" +
		"[ -f \"$f\" ] || { echo \"Error: $2 is not in the password store.\" >&2; echo second >&2; exit 1; }\ncat \"$f\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pass"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return entries, log
}

func put(t *testing.T, entries, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(entries, strings.ReplaceAll(name, "/", "_")), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func passRef(name string) config.SecretRef {
	return config.SecretRef{Vault: "main", Name: name, Backend: "pass"}
}

func TestPassSecretIsTheFirstLineAndKeepsTheSlashes(t *testing.T) {
	entries, log := stubPass(t)
	put(t, entries, "company/project/test", "the-value\nnotes: other\n")
	t.Setenv("PASSWORD_STORE_DIR", "/some/store")
	got, err := ReadSecret(passRef("company/project/test"), t.TempDir())
	if err != nil || got != "the-value" {
		t.Fatalf("got %q, %v", got, err)
	}
	logged, _ := os.ReadFile(log)
	if string(logged) != "show company/project/test\t/some/store\n" {
		t.Errorf("call = %q", logged)
	}
}

func TestPassLeavesPasswordStoreDirUnsetWhenTheUserDidNotSetIt(t *testing.T) {
	entries, log := stubPass(t)
	put(t, entries, "t", "v\n")
	os.Unsetenv("PASSWORD_STORE_DIR")
	if _, err := ReadSecret(passRef("t"), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if logged, _ := os.ReadFile(log); !strings.HasSuffix(string(logged), "\tunset\n") {
		t.Errorf("call = %q", logged)
	}
}

func TestPassMissingEntryCarriesTheFirstStderrLine(t *testing.T) {
	stubPass(t)
	_, err := ReadSecret(passRef("gone"), t.TempDir())
	var passErr *PassError
	if !errors.As(err, &passErr) || passErr.Message != "Error: gone is not in the password store." {
		t.Errorf("err = %v", err)
	}
}

func TestPassEmptyFirstLineIsEmpty(t *testing.T) {
	entries, _ := stubPass(t)
	put(t, entries, "e", "\nnote\n")
	if _, err := ReadSecret(passRef("e"), t.TempDir()); !errors.Is(err, ErrEmptySecret) {
		t.Errorf("err = %v", err)
	}
}

func TestPassDirectoryIsNotASecret(t *testing.T) {
	entries, _ := stubPass(t)
	put(t, entries, "company", "company\n├── project\n│   └── test\n└── other\n")
	_, err := ReadSecret(passRef("company"), t.TempDir())
	if !errors.Is(err, ErrNotASecret) || !strings.Contains(err.Error(), "company is a directory in the password store, not a secret") {
		t.Errorf("err = %v", err)
	}
	// A note that merely looks like a tree on its first line is still a secret.
	put(t, entries, "ok", "├── value\n")
	if got, err := ReadSecret(passRef("ok"), t.TempDir()); err != nil || got != "├── value" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestPassNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ReadSecret(passRef("t"), t.TempDir())
	if !errors.Is(err, ErrPassNotFound) || !strings.Contains(err.Error(), "pass") {
		t.Errorf("err = %v", err)
	}
}

func TestEnvSecret(t *testing.T) {
	ref := config.SecretRef{Vault: "main", Name: "EGZO_STACK_SECRET", Backend: "env"}
	t.Setenv("EGZO_STACK_SECRET", "")
	if _, err := ReadSecret(ref, ""); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("empty: %v", err)
	}
	t.Setenv("EGZO_STACK_SECRET", "v")
	if got, err := ReadSecret(ref, ""); err != nil || got != "v" {
		t.Errorf("got %q, %v", got, err)
	}
}
