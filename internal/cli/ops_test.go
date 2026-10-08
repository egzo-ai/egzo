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

func vaultYAML(dir string) string {
	return "vaults:\n  main:\n    backend: file\n    secrets:\n      KEY: {from: \"file:" + dir + "/KEY\"}\n      FROM_ENV: {from: \"env:EGZO_TEST_UNSET_VARIABLE\"}\n"
}

func TestSecretsSetWritesAPrivateFileAndLsNeverShowsTheValue(t *testing.T) {
	secrets := t.TempDir()
	yaml := vaultYAML(secrets)
	out, _, err := inProject(t, yaml, "hunter2-value\n", "secrets", "set", "main/KEY")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2-value") {
		t.Error("set printed the value")
	}
	info, err := os.Stat(filepath.Join(secrets, "KEY"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(filepath.Join(secrets, "KEY")); string(data) != "hunter2-value\n" {
		t.Errorf("file = %q", data)
	}
	listing, _, err := inProject(t, yaml, "", "secrets", "ls")
	if err != nil || strings.Contains(listing, "hunter2-value") {
		t.Fatalf("ls: %q, %v", listing, err)
	}
}

func TestSecretsLsShowsSetAndMissing(t *testing.T) {
	secrets := t.TempDir()
	os.WriteFile(filepath.Join(secrets, "KEY"), []byte("value\n"), 0o600)
	out, _, err := inProject(t, vaultYAML(secrets), "", "secrets", "ls")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		rows[strings.Fields(line)[0]] = line
	}
	if !strings.HasSuffix(rows["main/KEY"], "set") || !strings.HasSuffix(rows["main/FROM_ENV"], "missing") {
		t.Errorf("rows = %v", rows)
	}
}

func TestSecretsSetRefusesAnEnvironmentSourceAndAnUnknownSecret(t *testing.T) {
	yaml := vaultYAML(t.TempDir())
	_, _, err := inProject(t, yaml, "x\n", "secrets", "set", "main/FROM_ENV")
	if err == nil || !strings.Contains(err.Error(), "EGZO_TEST_UNSET_VARIABLE") {
		t.Errorf("err = %v", err)
	}
	_, _, err = inProject(t, yaml, "x\n", "secrets", "set", "main/NOPE")
	if err == nil || !strings.Contains(err.Error(), "main/KEY") {
		t.Errorf("err = %v", err)
	}
	_, _, err = inProject(t, yaml, "\n", "secrets", "set", "main/KEY")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("an empty value: err = %v", err)
	}
}

func TestSecretsRmDeletesTheFile(t *testing.T) {
	secrets := t.TempDir()
	os.WriteFile(filepath.Join(secrets, "KEY"), []byte("v\n"), 0o600)
	if _, _, err := inProject(t, vaultYAML(secrets), "", "secrets", "rm", "main/KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secrets, "KEY")); !os.IsNotExist(err) {
		t.Error("the file is still there")
	}
}

func TestProxyRulesListWhatEachAgentMayReachWithoutSecrets(t *testing.T) {
	yaml := `
vaults: {main: {backend: env, secrets: {S: {from: "env:S"}}}}
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

func TestDoctorChecksTheProjectFileWithoutAnEngine(t *testing.T) {
	var report []string
	collect := func(level, format string, a ...any) { report = append(report, level+" "+format) }
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)
	checkProject(&options{}, collect)
	if len(report) != 1 || !strings.HasPrefix(report[0], "ok no ") {
		t.Errorf("without a file: %v", report)
	}
	report = nil
	os.WriteFile(filepath.Join(dir, "egzo.yaml"), []byte("agents: [broken"), 0o644)
	checkProject(&options{}, collect)
	if len(report) != 1 || !strings.HasPrefix(report[0], "fail ") {
		t.Errorf("an invalid file: %v", report)
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
	sub, _, _ := cmd.Find([]string{"messages"})
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

func TestSendAnswerAndMessagesAreCommands(t *testing.T) {
	root := New()
	for _, name := range []string{"send", "answer", "messages", "questions"} {
		found, _, err := root.Find([]string{name})
		if err != nil || found.Name() != name {
			t.Errorf("command %s: %v", name, err)
		}
	}
	send, _, _ := root.Find([]string{"send"})
	for _, flag := range []string{"wait", "timeout", "interrupt"} {
		if send.Flags().Lookup(flag) == nil {
			t.Errorf("send has no --%s", flag)
		}
	}
	answer, _, _ := root.Find([]string{"answer"})
	if answer.Flags().Lookup("outcome") == nil {
		t.Error("answer has no --outcome")
	}
}
