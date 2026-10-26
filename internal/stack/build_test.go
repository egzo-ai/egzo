// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/egzo-ai/egzo/internal/config"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, text := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func buildAgent(args map[string]string) config.ResolvedAgent {
	return config.ResolvedAgent{Harness: "claude-code", Build: &config.ResolvedBuild{Context: "agent", Dockerfile: "Dockerfile", Args: args}}
}

func TestTheContextLeavesOutWhatDockerignoreExcludesButNeverTheDockerfile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Dockerfile": "FROM x\n", ".dockerignore": "Dockerfile\n.dockerignore\nlogs/\n*.tmp\n!keep.tmp\n",
		"src/main.go": "x", "logs/a.log": "x", "b.tmp": "x", "keep.tmp": "x",
	})
	files, err := readContext(root, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.rel)
	}
	sort.Strings(names)
	want := []string{".dockerignore", "Dockerfile", "keep.tmp", "src", "src/main.go"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("the context = %v, want %v", names, want)
	}

	var archive bytes.Buffer
	if err := writeContext(&archive, files); err != nil {
		t.Fatal(err)
	}
	headers := readArchive(t, archive.Bytes())
	if h := headers["src/main.go"]; h == nil || h.Uid != 0 || h.Gid != 0 || h.Uname != "" {
		t.Errorf("a file is sent owned by root: %+v", h)
	}
	if headers["src/"] == nil {
		t.Errorf("a directory is sent with its trailing slash: %v", headers)
	}
}

func TestTheTagFollowsTheContentNotTheTimestamps(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, filepath.Join(dir, "agent"), map[string]string{"Dockerfile": "FROM x\n", "note.txt": "one"})
	tag := func(agent config.ResolvedAgent) string {
		plan, err := planBuild("shop", dir, "coder", agent, "")
		if err != nil {
			t.Fatal(err)
		}
		return plan.Tag
	}
	first := tag(buildAgent(nil))
	if !strings.HasPrefix(first, BuildRepository+"shop-coder:") {
		t.Fatalf("tag = %s", first)
	}
	note := filepath.Join(dir, "agent", "note.txt")
	someTime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(note, someTime, someTime); err != nil {
		t.Fatal(err)
	}
	if tag(buildAgent(nil)) != first {
		t.Error("touching a file changed the tag")
	}
	if tag(buildAgent(map[string]string{"A": "1"})) == first {
		t.Error("an argument did not change the tag")
	}
	other := buildAgent(nil)
	other.Harness = "opencode"
	if tag(other) == first {
		t.Error("the harness image did not change the tag")
	}
	os.WriteFile(note, []byte("two"), 0o644)
	if tag(buildAgent(nil)) == first {
		t.Error("a changed file did not change the tag")
	}
}

func TestABuildNameIsARepositoryNameWhateverTheIdentifiers(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"shop", "coder"}: "shop-coder", {"my_shop", "a-"}: "my-shop-a", {"x_", "_y"}: "x---y", {"a", "b__"}: "a-b",
	} {
		if got := buildName(in[0], in[1]); got != want {
			t.Errorf("buildName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestABuildKitTraceIsPrintedAsItsStepsAndWhatTheyWrote(t *testing.T) {
	message := func(fields ...func([]byte) []byte) []byte {
		var b []byte
		for _, f := range fields {
			b = f(b)
		}
		return b
	}
	bytesField := func(n protowire.Number, v []byte) func([]byte) []byte {
		return func(b []byte) []byte { return protowire.AppendBytes(protowire.AppendTag(b, n, protowire.BytesType), v) }
	}
	vertex := message(bytesField(1, []byte("sha256:a")), bytesField(3, []byte("[2/2] RUN echo boom")))
	log := message(bytesField(1, []byte("sha256:a")), bytesField(4, []byte("boom\n\x1b]52;c;x\x07")))
	trace := message(bytesField(1, vertex), bytesField(1, vertex), bytesField(3, log))
	aux, _ := json.Marshal(base64.StdEncoding.EncodeToString(trace))
	stream := `{"id":"moby.buildkit.trace","aux":` + string(aux) + `}` + "\n" +
		`{"errorDetail":{"message":"process did not complete successfully: exit code: 3"},"error":"x"}` + "\n"

	var out bytes.Buffer
	err := readBuild(strings.NewReader(stream), &out)
	if err == nil || !strings.Contains(err.Error(), "exit code: 3") {
		t.Errorf("the build's error = %v", err)
	}
	if out.String() != "[2/2] RUN echo boom\nboom\n]52;c;x" {
		t.Errorf("printed %q: each step once, then what it wrote, with no escape sequence", out.String())
	}
}

func TestPodmansBuildOutputIsPrintedAsItComes(t *testing.T) {
	var out bytes.Buffer
	stream := `{"stream":"STEP 1/2: FROM x\n"}{"stream":"boom\n"}{"aux":{"ID":"sha256:1"}}`
	if err := readBuild(strings.NewReader(stream), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "STEP 1/2: FROM x\nboom\n" {
		t.Errorf("printed %q", out.String())
	}
}

func TestAMissingBaseImageIsReadFromTheBuildsError(t *testing.T) {
	text := "docker.io/library/alpine:3.19: failed to resolve source metadata for docker.io/library/alpine:3.19: no active sessions"
	if m := missingBase.FindStringSubmatch(text); m == nil || m[1] != "docker.io/library/alpine:3.19" {
		t.Errorf("missing base = %v", m)
	}
}
