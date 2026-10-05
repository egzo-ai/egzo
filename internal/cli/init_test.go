package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
)

func runInit(t *testing.T, dir, harness string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := initProject(dir, harness, cmd)
	return out.String(), err
}

// gitInit makes dir a git repository, optionally with an origin remote.
func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		if origin == "" && args[0] == "remote" {
			continue
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestOriginURL(t *testing.T) {
	const placeholder = "https://github.com/OWNER/REPO.git"
	cases := []struct {
		name, origin, want string
	}{
		{"https is kept", "https://github.com/acme/app.git", "https://github.com/acme/app.git"},
		{"https without suffix is kept", "https://gitlab.com/acme/app", "https://gitlab.com/acme/app"},
		{"scp style becomes https", "git@github.com:acme/app.git", "https://github.com/acme/app.git"},
		{"scp style without user", "github.com:acme/app", "https://github.com/acme/app.git"},
		{"ssh urls fall back", "ssh://git@github.com/acme/app.git", placeholder},
		{"local paths fall back", "/srv/git/app.git", placeholder},
		{"relative paths fall back", "../app", placeholder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir, c.origin)
			if got := originURL(dir); got != c.want {
				t.Errorf("originURL(%q) = %q, want %q", c.origin, got, c.want)
			}
		})
	}

	t.Run("no origin", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir, "")
		if got := originURL(dir); got != placeholder {
			t.Errorf("originURL = %q", got)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		if got := originURL(t.TempDir()); got != placeholder {
			t.Errorf("originURL = %q", got)
		}
	})
}

func TestScaffoldIsAValidProjectForEveryHarness(t *testing.T) {
	for _, harness := range config.HarnessNames() {
		t.Run(harness, func(t *testing.T) {
			text := scaffold(harness, "https://github.com/acme/app.git")
			file, err := config.Parse([]byte(text))
			if err != nil {
				t.Fatalf("the scaffold does not parse: %v\n%s", err, text)
			}
			if _, ok := file.Workspaces["repo"]; !ok {
				t.Errorf("no repo workspace:\n%s", text)
			}
			if len(file.Agents) != 1 {
				t.Errorf("agents = %v", file.Agents)
			}
			if _, _, err = config.Resolve(file, "demo", t.TempDir()); err != nil {
				t.Errorf("the scaffold does not resolve: %v\n%s", err, text)
			}
		})
	}
}

func TestScaffoldClaudeCodeCanReachItsAPI(t *testing.T) {
	file, err := config.Parse([]byte(scaffold("claude-code", "https://github.com/acme/app.git")))
	if err != nil {
		t.Fatal(err)
	}
	resolved, warnings, err := config.Resolve(file, "demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v: the first agent must be able to reach its provider", warnings)
	}
	if _, ok := resolved.Agents["claude"]; !ok {
		t.Errorf("agents = %v, want one named after the harness without -code", resolved.Agents)
	}
}

func TestInitCreatesTheFile(t *testing.T) {
	dir := t.TempDir()
	out, err := runInit(t, dir, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); err != nil {
		t.Fatalf("no file written: %v", err)
	}
	if !strings.Contains(out, "created egzo.yaml") || !strings.Contains(out, "next:") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, ".gitignore") {
		t.Errorf("outside a repository .gitignore must be left alone: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		t.Error("a .gitignore was created outside a git repository")
	}
}

func TestInitNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runInit(t, dir, "claude-code")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "mine\n" {
		t.Errorf("the existing file was modified: %q", data)
	}
}

func TestInitRejectsAnUnknownHarnessBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	_, err := runInit(t, dir, "gpt")
	if err == nil || !strings.Contains(err.Error(), `unknown harness "gpt"`) || !strings.Contains(err.Error(), "claude-code") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil {
		t.Error("a file was written for an unknown harness")
	}
}

func TestIgnoreWorkspaceData(t *testing.T) {
	cases := []struct {
		name      string
		existing  *string // nil: no .gitignore
		wantAdded bool
		want      string
	}{
		{"creates the file", nil, true, ".egzo/\n"},
		{"appends to an existing file", ptr("node_modules/\n"), true, "node_modules/\n.egzo/\n"},
		{"adds the missing newline first", ptr("node_modules/"), true, "node_modules/\n.egzo/\n"},
		{"is idempotent", ptr(".egzo/\n"), false, ".egzo/\n"},
		{"accepts the form without a slash", ptr("bin\n.egzo\n"), false, "bin\n.egzo\n"},
		{"ignores surrounding whitespace", ptr("  .egzo/  \n"), false, "  .egzo/  \n"},
		{"is not fooled by a similar entry", ptr(".egzo-cache/\n"), true, ".egzo-cache/\n.egzo/\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".gitignore")
			if c.existing != nil {
				if err := os.WriteFile(path, []byte(*c.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			added, err := ignoreWorkspaceData(dir)
			if err != nil {
				t.Fatal(err)
			}
			if added != c.wantAdded {
				t.Errorf("added = %v, want %v", added, c.wantAdded)
			}
			if got, _ := os.ReadFile(path); string(got) != c.want {
				t.Errorf(".gitignore = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("outside a repository nothing happens", func(t *testing.T) {
		dir := t.TempDir()
		added, err := ignoreWorkspaceData(dir)
		if err != nil || added {
			t.Errorf("added = %v, err = %v", added, err)
		}
	})
}

func TestInitInAGitRepositoryIgnoresTheWorkspaceData(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, "git@github.com:acme/app.git")
	out, err := runInit(t, dir, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "added .egzo/ to .gitignore") {
		t.Errorf("output = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "https://github.com/acme/app.git") {
		t.Errorf("the scaffold does not use the origin:\n%s", data)
	}
}

func ptr(s string) *string { return &s }

func TestContains(t *testing.T) {
	sorted := []string{"a", "c", "e"}
	for value, want := range map[string]bool{"a": true, "c": true, "e": true, "b": false, "z": false, "": false} {
		if got := contains(sorted, value); got != want {
			t.Errorf("contains(%q) = %v, want %v", value, got, want)
		}
	}
	if contains(nil, "a") {
		t.Error("contains(nil)")
	}
}

func TestScaffoldQuotesAnUnusualURL(t *testing.T) {
	for _, url := range []string{"https://h/o/r.git#frag", "https://h/o/r.git?a=b, c: d", "https://h/{x}.git"} {
		file, err := config.Parse([]byte(scaffold("claude-code", url)))
		if err != nil || file.Workspaces["repo"].Git.URL != url {
			t.Errorf("%s: err = %v, url = %v", url, err, file.Workspaces["repo"].Git)
		}
	}
}

func TestWithoutCredentials(t *testing.T) {
	cases := map[string]string{
		"https://alice:ghp_secret@github.com/a/b.git": "https://github.com/a/b.git",
		"https://ghp_secret@github.com/a/b.git":       "https://github.com/a/b.git",
		"https://github.com/a/b.git":                  "https://github.com/a/b.git",
		"https://":                                    placeholderURL,
	}
	for in, want := range cases {
		if got := withoutCredentials(in); got != want {
			t.Errorf("withoutCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}
