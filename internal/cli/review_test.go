// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

func TestMessagesAreListedAsHarmlessSingleLines(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := writeMessages(cmd, []messageRow{
		{ID: "m1", From: "agent:a", To: "operator", Kind: "request", State: "queued", Text: "hello\n\x1b]52;c;ZXZpbA==\x07world\x1b[2J" + strings.Repeat("é", 100)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 {
		t.Errorf("a message forged rows: %q", out.String())
	}
	if !strings.Contains(out.String(), "...") || !isValidUTF8(out.String()) {
		t.Errorf("long text was cut badly: %q", out.String())
	}
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestAnAnswerIsPrintedWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	printUntrusted(&out, "done\x1b[31m red\x1b[0m\x07\nsecond line")
	if strings.ContainsAny(out.String(), "\x1b\x07") || !strings.Contains(out.String(), "second line") {
		t.Errorf("output = %q", out.String())
	}
}

// --- secrets --------------------------------------------------------------------------------------------------------

func TestSecretStatesSayWhyASecretIsNotUsable(t *testing.T) {
	t.Setenv("EGZO_REVIEW_SET", "v")
	if got, _ := secretState(config.SecretRef{Vault: "main", Name: "EGZO_REVIEW_SET", Backend: "env"}, t.TempDir()); got != "set" {
		t.Errorf("set: %q", got)
	}
	t.Setenv("EGZO_REVIEW_UNSET", "")
	if got, _ := secretState(config.SecretRef{Vault: "main", Name: "EGZO_REVIEW_UNSET", Backend: "env"}, t.TempDir()); got != "missing" {
		t.Errorf("env: %q", got)
	}
}

// --- down --workspaces --------------------------------------------------------------------------------------------------

func TestOnlyCheckoutsAreOfferedForRemoval(t *testing.T) {
	root := t.TempDir()
	project := &config.Resolved{
		Name:       "p",
		Workspaces: map[string]config.ResolvedWorkspace{"repo": {Git: &config.Git{URL: "https://h/r"}, Mode: "clone", Path: filepath.Join(root, "ws")}},
		Agents: map[string]config.ResolvedAgent{
			"coder":    {Workspaces: []config.Mount{{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}}},
			"reviewer": {Workspaces: []config.Mount{{Name: "repo", Mount: "/workspace/repo", Mode: "rw"}}},
		},
	}
	os.MkdirAll(filepath.Join(root, "ws", "coder", ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "ws", "reviewer"), 0o755) // no .git: not a checkout
	os.WriteFile(filepath.Join(root, "ws", "reviewer", "precious"), []byte("x"), 0o644)
	os.Symlink("/", filepath.Join(root, "ws", "link")) // must be refused, with the reason
	os.WriteFile(filepath.Join(root, "ws", ".egzo-checkouts"), []byte("coder\nreviewer\nlink\n"), 0o644)
	ok, skipped := removableCheckouts(project, root)
	if len(ok) != 1 || !strings.HasSuffix(ok[0], "coder") {
		t.Errorf("removable = %v (a directory that is not a checkout is never even offered)", ok)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "link") || !strings.Contains(skipped[0], "symbolic link") {
		t.Errorf("skipped = %v", skipped)
	}
}

var _ = stack.Unsaved{}

func TestChownTouchesOnlyWhatIsNotAlreadyTheirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(root, "a", "b", "f"), []byte("x"), 0o644)
	var changed []string
	record := func(path string, uid, gid int) error { changed = append(changed, path); return nil }
	// everything here belongs to the test's own user: nothing to do
	if err := chownTree(root, os.Getuid(), os.Getgid(), record); err != nil || len(changed) != 0 {
		t.Fatalf("changed %v (%v) although everything already belonged to the owner", changed, err)
	}
	// anything else is changed, the root and every entry below it
	if err := chownTree(root, os.Getuid()+1, os.Getgid(), record); err != nil || len(changed) != 4 {
		t.Errorf("changed %d entries, want 4: %v (%v)", len(changed), changed, err)
	}
}
