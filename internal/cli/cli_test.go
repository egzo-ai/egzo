package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
)

func TestExitError(t *testing.T) {
	err := ExitError{Code: 3}
	if got := err.Error(); got != "command exited with code 3" {
		t.Errorf("Error() = %q", got)
	}

	code, ok := IsExitError(fmt.Errorf("while running: %w", err))
	if !ok || code != 3 {
		t.Errorf("IsExitError(wrapped) = %d, %v", code, ok)
	}
	if code, ok := IsExitError(errors.New("other")); ok || code != 0 {
		t.Errorf("IsExitError(other) = %d, %v", code, ok)
	}
	if _, ok := IsExitError(nil); ok {
		t.Error("IsExitError(nil) = true")
	}
}

func TestRootCommandTree(t *testing.T) {
	root := New()
	want := []string{
		"init", "config", "up", "down", "ps", "send", "events", "questions", "answer",
		"logs", "exec", "start", "stop", "restart", "control", "proxy",
		"attach", "agent", "hook", "prep", "version", "secrets", "doctor", "diff", "ca",
	}
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("command %q is missing", name)
		}
	}

	flag := root.PersistentFlags().Lookup("project-name")
	if flag == nil || flag.Shorthand != "p" {
		t.Errorf("--project-name flag = %+v", flag)
	}
	if !root.SilenceUsage || !root.SilenceErrors {
		t.Error("main prints errors itself, so cobra must stay silent")
	}
}

func TestCommandsRejectUnexpectedArguments(t *testing.T) {
	for _, name := range []string{"init", "config"} {
		root := New()
		root.SetArgs([]string{name, "surprise"})
		root.SetOut(new(strings.Builder))
		root.SetErr(new(strings.Builder))
		if err := root.Execute(); err == nil {
			t.Errorf("%s accepted a positional argument", name)
		}
	}
}

func writeProject(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func TestLoadProject(t *testing.T) {
	t.Run("name precedence follows the flag, the environment, the file", func(t *testing.T) {
		dir := writeProject(t, "name: from-file\n")
		for _, c := range []struct{ flag, env, want string }{
			{"", "", "from-file"},
			{"", "from-env", "from-env"},
			{"from-flag", "from-env", "from-flag"},
		} {
			t.Setenv(config.EnvProjectName, c.env)
			p, err := loadProject(&options{projectName: c.flag})
			if err != nil {
				t.Fatal(err)
			}
			if p.Resolved.Name != c.want {
				t.Errorf("flag %q env %q: name = %q, want %q", c.flag, c.env, p.Resolved.Name, c.want)
			}
			if p.Dir != dir && p.Dir != mustEvalSymlinks(t, dir) {
				t.Errorf("Dir = %q, want %q", p.Dir, dir)
			}
		}
	})

	t.Run("falls back to the directory name", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "My App")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.FileName), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		t.Setenv(config.EnvProjectName, "")
		p, err := loadProject(&options{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Resolved.Name != "my-app" {
			t.Errorf("name = %q, want my-app", p.Resolved.Name)
		}
	})

	t.Run("surfaces warnings", func(t *testing.T) {
		writeProject(t, "name: x\nagents:\n  a: { harness: claude-code }\n")
		p, err := loadProject(&options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Warnings) != 1 {
			t.Errorf("warnings = %v", p.Warnings)
		}
	})

	t.Run("errors", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if _, err := loadProject(&options{}); err == nil || !strings.Contains(err.Error(), "no egzo.yaml") {
			t.Errorf("missing file: err = %v", err)
		}
		writeProject(t, "name: x\nagents:\n  a: {}\n")
		if _, err := loadProject(&options{}); err == nil || !strings.Contains(err.Error(), "harness is required") {
			t.Errorf("invalid file: err = %v", err)
		}
		writeProject(t, "name: x\n")
		if _, err := loadProject(&options{projectName: "bad name"}); err == nil || !strings.Contains(err.Error(), "invalid project name") {
			t.Errorf("invalid name: err = %v", err)
		}
	})
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestTheSameProjectThroughASymlinkIsTheSameProject(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "egzo.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	viaLink, err := loadProject(&options{file: filepath.Join(link, "egzo.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := loadProject(&options{file: filepath.Join(real, "egzo.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if viaLink.Dir != direct.Dir {
		t.Errorf("Dir through the link = %q, direct = %q", viaLink.Dir, direct.Dir)
	}
}
