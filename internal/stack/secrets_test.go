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

func envRef(name string) config.SecretRef {
	return config.SecretRef{Vault: "main", Name: name, Backend: "env"}
}

// unsetenv unsets a variable for the test; t.Setenv restores it after.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	os.Unsetenv(name)
}

func writeDotenv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotenvFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnvSecretFromDotenvWhenTheEnvironmentDoesNotHaveIt(t *testing.T) {
	unsetenv(t, "EGZO_STACK_SECRET")
	dir := writeDotenv(t, "EGZO_STACK_SECRET=from-file\n")
	if got, err := ReadSecret(envRef("EGZO_STACK_SECRET"), dir); err != nil || got != "from-file" {
		t.Errorf("got %q, %v", got, err)
	}
	if !FromDotenv(envRef("EGZO_STACK_SECRET"), dir) {
		t.Error("FromDotenv = false")
	}
}

func TestEnvSecretTheEnvironmentWinsOverDotenvEvenEmpty(t *testing.T) {
	dir := writeDotenv(t, "EGZO_STACK_SECRET=from-file\n")
	t.Setenv("EGZO_STACK_SECRET", "from-shell")
	if got, err := ReadSecret(envRef("EGZO_STACK_SECRET"), dir); err != nil || got != "from-shell" {
		t.Errorf("got %q, %v", got, err)
	}
	if FromDotenv(envRef("EGZO_STACK_SECRET"), dir) {
		t.Error("FromDotenv = true")
	}
	t.Setenv("EGZO_STACK_SECRET", "")
	_, err := ReadSecret(envRef("EGZO_STACK_SECRET"), dir)
	if err == nil || !strings.Contains(err.Error(), "hides the one in .env") {
		t.Errorf("empty: %v", err)
	}
}

func TestEnvSecretEmptyInDotenvIsEmpty(t *testing.T) {
	unsetenv(t, "EGZO_STACK_SECRET")
	_, err := ReadSecret(envRef("EGZO_STACK_SECRET"), writeDotenv(t, "EGZO_STACK_SECRET=\n"))
	if !errors.Is(err, ErrEmptySecret) {
		t.Errorf("err = %v", err)
	}
}

func TestDotenvErrorsNeverHoldAValue(t *testing.T) {
	unsetenv(t, "EGZO_STACK_SECRET")
	unsetenv(t, "EGZO_STACK_UNSET")
	cases := map[string]string{
		"bad name":     "OK=1\nBAD@NAME=the-value\n",
		"key space":    "MY KEY=the-value\n",
		"unterminated": "EGZO_STACK_SECRET=\"the-value\n",
		"template":     "EGZO_STACK_SECRET=the-value${\n",
		"required":     "EGZO_STACK_SECRET=${EGZO_STACK_UNSET:?the-value}\n",
	}
	for name, content := range cases {
		_, err := ReadSecret(envRef("EGZO_STACK_SECRET"), writeDotenv(t, content))
		var problem *DotenvError
		if !errors.As(err, &problem) || strings.Contains(err.Error(), "the-value") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestNoDotenvOrADirectoryIsNoVariable(t *testing.T) {
	unsetenv(t, "EGZO_STACK_SECRET")
	dir := t.TempDir()
	for _, prepare := range []func(){func() {}, func() { os.Mkdir(filepath.Join(dir, DotenvFile), 0o755) }} {
		prepare()
		_, err := ReadSecret(envRef("EGZO_STACK_SECRET"), dir)
		if err == nil || !strings.Contains(err.Error(), "is not set") {
			t.Errorf("err = %v", err)
		}
	}
}
