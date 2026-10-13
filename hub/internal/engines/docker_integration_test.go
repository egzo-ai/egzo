//go:build integration

package engines_test

// The Docker engine against a real engine and a real project made by the egzo CLI: what the hub's own tests fake.
//
//	make hub-integration     builds bin/egzo and runs this
//
// It needs EGZO_IT_BIN (the CLI) and a Docker engine at DOCKER_HOST.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/internal/engine"
)

func sh(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s%s", name, strings.Join(args, " "), err, out.String(), errOut.String())
	}
	return out.String()
}

// image builds a small image from a Dockerfile and files, tagged by their content.
func image(t *testing.T, prefix string, files map[string][]byte, dockerfile string) string {
	t.Helper()
	sum := sha256.New()
	sum.Write([]byte(dockerfile))
	for _, name := range []string{"egzo", "fake-tui"} {
		sum.Write(files[name])
	}
	tag := prefix + ":" + hex.EncodeToString(sum.Sum(nil))[:12]
	if exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return tag
	}
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644)
	sh(t, dir, nil, "docker", "build", "-q", "-t", tag, ".")
	return tag
}

func TestDockerEngineAgainstARealProject(t *testing.T) {
	bin := os.Getenv("EGZO_IT_BIN")
	if bin == "" {
		t.Skip("EGZO_IT_BIN is not set (make hub-integration sets it)")
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	tui, err := os.ReadFile("../../../specs/fixtures/fake-tui.sh")
	if err != nil {
		t.Fatal(err)
	}
	sidecar := image(t, "egzo-hub-it", map[string][]byte{"egzo": binary},
		"FROM docker.io/library/alpine:3\nRUN apk add --no-cache ca-certificates git\nCOPY egzo /egzo\n")
	session := image(t, "egzo-hub-it-session", map[string][]byte{"egzo": binary, "fake-tui": tui},
		"FROM docker.io/library/alpine:3\nRUN apk add --no-cache curl git\nCOPY fake-tui /usr/local/bin/fake-tui\nCOPY egzo /usr/local/bin/egzo\n"+
			"ENTRYPOINT [\"/usr/local/bin/egzo\", \"agent\", \"run\", \"--\"]\nCMD [\"/usr/local/bin/fake-tui\"]\n")

	name := fmt.Sprintf("hubit%d", time.Now().UnixNano()%1_000_000)
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "egzo.yaml"), []byte("agents:\n  coder:\n    harness: custom\n    image: "+session+"\n    env:\n      FAKE_TUI: cat\n"), 0o644)
	env := []string{"EGZO_IMAGE=" + sidecar, "NO_COLOR=1", "EGZO_PROJECT_NAME="}
	egzo := func(args ...string) string { return sh(t, dir, env, bin, args...) }
	egzo("project", "up")
	t.Cleanup(func() { exec.Command(bin, "project", "down", "--force").Run(); cleanup(dir, env, bin) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := engine.Dial(ctx, engine.Target{Host: os.Getenv("DOCKER_HOST")})
	if err != nil {
		client, err = engine.Connect(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	var eng engines.Engine = engines.NewDocker(client)
	defer eng.Close()

	// discovery
	projects, err := eng.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *engines.Project
	for i := range projects {
		if projects[i].Name == name {
			found = &projects[i]
		}
	}
	if found == nil || !found.Up || len(found.Agents) != 0 {
		t.Fatalf("project %s in %+v", name, projects)
	}
	detail, err := eng.Project(ctx, name)
	if err != nil || len(detail.Templates) != 1 || detail.Templates[0] != "coder" || !detail.Up {
		t.Fatalf("project = %+v, %v", detail, err)
	}
	if _, err := eng.Project(ctx, "no-such-project-here"); err == nil {
		t.Error("an unknown project was found")
	} else if _, ok := err.(engines.ErrNoProject); !ok {
		t.Errorf("err = %T %v", err, err)
	}

	// spawn on behalf of a user: the actor label is what the hub's own-agents rule reads
	spawned, err := eng.Spawn(ctx, name, "coder", "", "user:u1")
	if err != nil || spawned != "coder-1" {
		t.Fatalf("spawn = %q, %v", spawned, err)
	}
	if _, err := eng.Spawn(ctx, name, "coder", "coder-1", "user:u1"); err == nil {
		t.Error("spawned an instance name twice")
	}
	if _, err := eng.Spawn(ctx, name, "coder", "bad", "someone"); err == nil {
		t.Error("an invalid actor was accepted")
	}
	detail, _ = eng.Project(ctx, name)
	if len(detail.Agents) != 1 || detail.Agents[0].Actor != "user:u1" || detail.Agents[0].Template != "coder" || !detail.Agents[0].Running() {
		t.Fatalf("agents = %+v", detail.Agents)
	}

	// the cli sees the same agent
	if ps := egzo("ps"); !strings.Contains(ps, "user:u1") {
		t.Errorf("egzo ps does not show the actor:\n%s", ps)
	}

	// stop and start
	if err := eng.Stop(ctx, name, "coder-1"); err != nil {
		t.Fatal(err)
	}
	if detail, _ = eng.Project(ctx, name); detail.Agents[0].Running() || detail.Agents[0].Activity != "stopped" {
		t.Errorf("after stop: %+v", detail.Agents[0])
	}
	if err := eng.Start(ctx, name, "coder-1"); err != nil {
		t.Fatal(err)
	}

	// the terminal: bytes in, bytes out, through exec attach
	in, inWriter := io.Pipe()
	out := &syncBuffer{}
	resize := make(chan engines.Size, 1)
	attachCtx, stopAttach := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- eng.Attach(attachCtx, name, "coder-1", engines.Terminal{In: in, Out: out, Size: engines.Size{Cols: 100, Rows: 30}, Resize: resize})
	}()
	waitFor(t, "READY from the terminal", func() bool { return strings.Contains(out.String(), "READY") })
	inWriter.Write([]byte("hello from the hub\r"))
	waitFor(t, "the echo", func() bool { return strings.Count(out.String(), "hello from the hub") >= 1 })
	stopAttach()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("attach did not end when its context was cancelled")
	}

	// events: typed, with no message texts; the spec snapshot; logs
	events, err := eng.Events(ctx, name, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type == "hook" {
			t.Errorf("a hook event leaked into the timeline: %+v", e)
		}
	}
	spec, err := eng.Spec(ctx, name)
	if err != nil || !strings.Contains(string(spec), name) || strings.Contains(string(spec), "SECRET") {
		t.Errorf("spec = %q, %v", spec, err)
	}
	if logs, err := eng.Logs(ctx, name, "coder-1", 20); err != nil {
		t.Errorf("logs: %v", err)
	} else {
		t.Logf("logs of the agent: %q", logs)
	}
	if _, err := eng.Logs(ctx, name, "control", 5); err != nil {
		t.Errorf("logs of a sidecar: %v", err)
	}

	// remove
	if err := eng.Stop(ctx, name, "coder-1"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Remove(ctx, name, "coder-1"); err != nil {
		t.Fatal(err)
	}
	if detail, _ = eng.Project(ctx, name); len(detail.Agents) != 0 {
		t.Errorf("agents after rm: %+v", detail.Agents)
	}
	if err := eng.Remove(ctx, name, "coder-1"); err == nil {
		t.Error("removed an agent twice")
	}
}

func cleanup(dir string, env []string, bin string) {
	cmd := exec.Command(bin, "project", "down", "--force")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	cmd.Run()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
