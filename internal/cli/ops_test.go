// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inProject runs egzo in a directory holding the given egzo.yaml and returns what it printed.
func inProject(t *testing.T, yaml string, stdin string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "egzo.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	var out, errOut bytes.Buffer
	root := New()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func vaultYAML() string {
	return "vaults:\n  main:\n    backend: env\n" +
		"egress:\n  default:\n    services:\n" +
		"      set: { hosts: [set.example.com], inject: { header: X-Key }, secret: main/EGZO_TEST_SET_VARIABLE }\n" +
		"      unset: { hosts: [unset.example.com], inject: { header: X-Key }, secret: main/EGZO_TEST_UNSET_VARIABLE }\n"
}

func TestProjectCheckShowsSetAndMissingSecretsAndExitsOneForAMissingOne(t *testing.T) {
	t.Setenv("EGZO_TEST_SET_VARIABLE", "value")
	t.Setenv("EGZO_TEST_UNSET_VARIABLE", "")
	out, _, err := inProject(t, vaultYAML(), "", "project", "check")
	if code, ok := IsExitError(err); !ok || code != 1 {
		t.Fatalf("err = %v, want exit code 1 for a secret that is empty", err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "secret ") {
			rows[strings.Fields(line)[2]] = line
		}
	}
	if !strings.HasPrefix(rows["main/EGZO_TEST_SET_VARIABLE"], "ok") || !strings.HasPrefix(rows["main/EGZO_TEST_UNSET_VARIABLE"], "fail") {
		t.Errorf("rows = %v", rows)
	}
	if strings.Contains(out, "value") {
		t.Errorf("check printed a value: %q", out)
	}
}

func TestProxyRulesListWhatEachAgentMayReachWithoutSecrets(t *testing.T) {
	yaml := `
vaults: {main: {backend: env}}
egress:
  default: {allow: [docs.example.org]}
  wide:
    extend: default
    services: {svc: {hosts: [api.test], inject: {header: x-key}, secret: main/S}}
agents:
  coder: {harness: custom, image: x}
  reviewer: {harness: custom, image: x, egress: wide}
`
	out, _, err := inProject(t, yaml, "", "proxy", "rules")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docs.example.org", "api.test", "x-key", "main/S"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules lack %q:\n%s", want, out)
		}
	}
	only, _, err := inProject(t, yaml, "", "proxy", "rules", "--agent", "coder")
	if err != nil || strings.Contains(only, "api.test") || !strings.Contains(only, "docs.example.org") {
		t.Errorf("rules for coder: %q, %v", only, err)
	}
	if _, _, err := inProject(t, yaml, "", "proxy", "rules", "--agent", "ghost"); err == nil || !strings.Contains(err.Error(), "coder") {
		t.Errorf("an unknown agent: %v", err)
	}
}

func TestAgentLinesKeepsOnlyOneAgentsAuditLinesAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	filter := &agentLines{out: &out, agent: "coder"}
	filter.Write([]byte(`{"agent":"coder","host":"a"}` + "\n" + `{"agent":"reviewer","host":"b"}` + "\n" + `{"agent":"cod`))
	filter.Write([]byte(`er","host":"c"}` + "\nnot json\n"))
	if got := out.String(); got != `{"agent":"coder","host":"a"}`+"\n"+`{"agent":"coder","host":"c"}`+"\n" {
		t.Errorf("got %q", got)
	}
}

func TestProjectCheckNeedsAFileAndFailsOnAnInvalidOne(t *testing.T) {
	t.Chdir(t.TempDir())
	root := New()
	root.SetArgs([]string{"project", "check"})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	if err := root.Execute(); err == nil {
		t.Error("check without a project file succeeded")
	}
	out, _, err := inProject(t, "agents: [broken", "", "project", "check")
	if code, ok := IsExitError(err); !ok || code != 1 || !strings.HasPrefix(out, "fail ") {
		t.Errorf("an invalid file: %q, %v", out, err)
	}
}

func TestMinimumGit(t *testing.T) {
	for output, want := range map[string]bool{
		"git version 2.53.0\n": true, "git version 2.48.1": true, "git version 2.47.3": false,
		"git version 3.0.0": true, "git version 1.9.9": false, "nonsense": false,
	} {
		if _, got := gitAtLeast(output); got != want {
			t.Errorf("%q: %v, want %v", output, got, want)
		}
	}
}

func TestTheProjectFileIsFoundFromASubdirectoryAndNamedWithFlagOrEnv(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	os.MkdirAll(deep, 0o755)
	file := filepath.Join(root, "egzo.yaml")
	os.WriteFile(file, []byte("agents: {}\n"), 0o644)
	other := filepath.Join(t.TempDir(), "other.yaml")
	os.WriteFile(other, []byte("agents: {}\n"), 0o644)

	old, _ := os.Getwd()
	defer os.Chdir(old)
	os.Chdir(deep)
	got, err := projectFile(&options{})
	if resolved, _ := filepath.EvalSymlinks(got); err != nil || resolved != mustEval(t, file) {
		t.Errorf("from a subdirectory: %q, %v", got, err)
	}
	if got, _ := projectFile(&options{file: other}); got != other {
		t.Errorf("with -f: %q", got)
	}
	t.Setenv("EGZO_FILE", other)
	if got, _ := projectFile(&options{}); got != other {
		t.Errorf("with EGZO_FILE: %q", got)
	}
	if got, _ := projectFile(&options{file: file}); got != file {
		t.Errorf("-f must win over EGZO_FILE: %q", got)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestLineWatcherHandsOverCompleteLinesAcrossWrites(t *testing.T) {
	var lines []string
	watcher := &lineWatcher{handle: func(line []byte) { lines = append(lines, string(line)) }}
	watcher.Write([]byte("one\ntw"))
	watcher.Write([]byte("o\nthree"))
	watcher.Write([]byte("\n"))
	if strings.Join(lines, "|") != "one|two|three" {
		t.Errorf("lines = %v", lines)
	}
}

func TestMessagesAreListedWithTheirStateAndAShortenedText(t *testing.T) {
	var out bytes.Buffer
	cmd := New()
	cmd.SetOut(&out)
	sub, _, _ := cmd.Find([]string{"message", "ls"})
	sub.SetOut(&out)
	long := strings.Repeat("word ", 30)
	err := writeMessages(sub, []messageRow{
		{ID: "m1", From: "operator", To: "agent:coder", Kind: "request", State: "queued", Text: "short\ntext"},
		{ID: "m2", From: "agent:coder", To: "operator", Kind: "question", State: "queued", Text: long},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "short text") {
		t.Fatalf("output = %q", out.String())
	}
	if !strings.HasSuffix(lines[2], "...") || strings.Count(lines[2], "word") > 14 {
		t.Errorf("a long text must be cut: %q", lines[2])
	}
}

func TestMessageCommandsLiveUnderMessageWithAnAlias(t *testing.T) {
	root := New()
	for _, name := range []string{"send", "ls", "reply"} {
		found, _, err := root.Find([]string{"message", name})
		if err != nil || found.Name() != name {
			t.Errorf("command message %s: %v", name, err)
		}
	}
	if found, _, err := root.Find([]string{"msg", "ls"}); err != nil || found.Name() != "ls" {
		t.Errorf("the alias msg: %v", err)
	}
	send, _, _ := root.Find([]string{"message", "send"})
	for _, flag := range []string{"wait", "timeout", "interrupt"} {
		if send.Flags().Lookup(flag) == nil {
			t.Errorf("send has no --%s", flag)
		}
	}
	reply, _, _ := root.Find([]string{"message", "reply"})
	if reply.Flags().Lookup("outcome") == nil {
		t.Error("reply has no --outcome")
	}
	list, _, _ := root.Find([]string{"message", "ls"})
	for _, flag := range []string{"kind", "state", "to", "from", "all"} {
		if list.Flags().Lookup(flag) == nil {
			t.Errorf("ls has no --%s", flag)
		}
	}
}
