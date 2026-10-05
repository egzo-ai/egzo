package stack

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// --- R-30: a container that was created and never started is recreated, not just started -------------------

func TestAContainerThatWasCreatedButNeverStartedIsRecreated(t *testing.T) {
	desired := Desired{Containers: []ContainerSpec{ctr("p-coder-1", "c")}}
	observed := Observed{Resources: []Resource{{Type: "container", Name: "p-coder-1", ID: "id1", ConfigHash: "c", State: "created"}}}
	got := verbs(BuildPlan(desired, observed, false))
	want := []string{"remove container p-coder-1", "create container p-coder-1"}
	if !slices.Equal(got, want) {
		t.Errorf("plan = %v, want %v: a plain start would skip the volume hand-over the interrupted run never did", got, want)
	}
	observed.Resources[0].State = "exited"
	if got := verbs(BuildPlan(desired, observed, false)); !slices.Equal(got, []string{"start container p-coder-1"}) {
		t.Errorf("an exited container that ran before is just started: %v", got)
	}
}

// --- R-35: agents start after the sidecars are up and attached to their network ----------------------------------

func TestAnAgentIsCreatedOnlyAfterTheSidecarsAreHealthyAndOnItsNetwork(t *testing.T) {
	project := desireProject()
	desired := desire(t, project)
	plan := BuildPlan(desired, Observed{}, false)
	deps := dependencies(desired, plan)
	index := func(text string) int {
		for i, a := range plan {
			if a.String() == text {
				return i
			}
		}
		t.Fatalf("no action %q in %v", text, verbs(plan))
		return -1
	}
	for _, agent := range []string{"coder", "review"} {
		create := index("create container proj-" + agent + "-1")
		for _, need := range []string{
			"create container proj-control-1", "create container proj-proxy-1",
			"connect network proj_" + agent + " to proj-control-1", "connect network proj_" + agent + " to proj-proxy-1",
		} {
			if !reaches(deps, create, index(need)) {
				t.Errorf("agent %s is created without waiting for %q", agent, need)
			}
		}
	}
	if reaches(deps, index("connect network proj_coder to proj-proxy-1"), index("create container proj-coder-1")) {
		t.Error("the connect depends on the agent it serves: a cycle")
	}
}

// reaches reports whether action `from` waits, directly or not, for action `to`.
func reaches(deps [][]int, from, to int) bool {
	seen := map[int]bool{}
	var walk func(int) bool
	walk = func(i int) bool {
		for _, d := range deps[i] {
			if d == to || (!seen[d] && walk(d)) {
				return true
			}
			seen[d] = true
		}
		return false
	}
	return walk(from)
}

func TestTheDependencyGraphOfARealProjectHasNoCycle(t *testing.T) {
	project := desireProject()
	desired := desire(t, project)
	plan := BuildPlan(desired, Observed{}, false)
	deps := dependencies(desired, plan)
	for i := range plan {
		if reaches(deps, i, i) {
			t.Errorf("%s waits for itself", plan[i])
		}
	}
}

// --- R-27: limits and unprivileged sidecars ----------------------------------------------------------------------

func TestSidecarsRunUnprivilegedWithLimitsAndBoundedLogs(t *testing.T) {
	d := desire(t, desireProject())
	for _, name := range []string{"proj-control-1", "proj-proxy-1"} {
		c := findContainer(t, d, name)
		if c.User == "" || c.User == "0" || strings.HasPrefix(c.User, "0:") || strings.HasPrefix(c.User, "root") {
			t.Errorf("%s runs as %q", name, c.User)
		}
		if c.Memory <= 0 || c.PidsLimit <= 0 {
			t.Errorf("%s limits: memory %d pids %d", name, c.Memory, c.PidsLimit)
		}
		if len(c.OwnedVolumes) == 0 {
			t.Errorf("%s writes volumes it does not own", name)
		}
		if !c.BoundedLogs {
			t.Errorf("%s logs are unbounded", name)
		}
	}
	control := findContainer(t, d, "proj-control-1")
	if !slices.Contains(control.OwnedVolumes, "proj_control") {
		t.Errorf("control volumes = %v", control.OwnedVolumes)
	}
	proxy := findContainer(t, d, "proj-proxy-1")
	if !slices.Contains(proxy.OwnedVolumes, "proj_ca") || !slices.Contains(proxy.OwnedVolumes, "proj_ca-private") {
		t.Errorf("proxy volumes = %v", proxy.OwnedVolumes)
	}
	for target, options := range proxy.Tmpfs {
		if !strings.Contains(options, "uid="+strings.Split(proxy.User, ":")[0]) {
			t.Errorf("tmpfs %s (%s) is not writable by %s", target, options, proxy.User)
		}
	}
}

func TestAgentsGetAProcessLimitUnlessTheFileSetsOne(t *testing.T) {
	d := desire(t, desireProject())
	if c := findContainer(t, d, "proj-coder-1"); c.PidsLimit != defaultAgentPids || !c.BoundedLogs {
		t.Errorf("pids = %d, bounded logs = %v", c.PidsLimit, c.BoundedLogs)
	}
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Resources = config.Resources{Pids: 77, Memory: "1g"}
	project.Agents["coder"] = coder
	c := findContainer(t, desire(t, project), "proj-coder-1")
	if c.PidsLimit != 77 {
		t.Errorf("pids = %d", c.PidsLimit)
	}
	if c.MemorySwap != c.Memory || c.Memory == 0 {
		t.Errorf("memory %d swap %d: a memory limit must not be doubled by swap", c.Memory, c.MemorySwap)
	}
}

// --- R-29: who agents run as ----------------------------------------------------------------------------------------

func TestWhoAgentsRunAs(t *testing.T) {
	cases := []struct {
		name     string
		client   engine.Client
		uid, gid int
		want     string
	}{
		{"an ordinary user", engine.Client{}, 1000, 1000, "1000:1000"},
		{"root is never an agent's user", engine.Client{}, 0, 0, "1000:1000"},
		{"podman maps users itself", engine.Client{Podman: true}, 1000, 1000, ""},
		{"rootless docker maps users itself", engine.Client{Rootless: true}, 1000, 1000, ""},
		{"no uid on this platform", engine.Client{}, -1, -1, ""},
	}
	for _, c := range cases {
		if got := agentUser(&c.client, c.uid, c.gid); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// --- R-39: the content of the prompt is part of what the agent is ---------------------------------------------------

func TestChangingThePromptChangesTheAgentsHash(t *testing.T) {
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Prompt = "./prompts/coder.md"
	project.Agents["coder"] = coder
	hashOf := func(digest string) string {
		in := desireInputs
		in.PromptDigests = map[string]string{"coder": digest}
		d, err := Desire(project, "/dir", in)
		if err != nil {
			t.Fatal(err)
		}
		return findContainer(t, d, "proj-coder-1").Identity.ConfigHash
	}
	if hashOf("aaa") == hashOf("bbb") {
		t.Error("editing the prompt file did not change the agent's hash: `up` would leave a stale agent running")
	}
	first, second := hashOf("aaa"), hashOf("aaa")
	if first != second {
		t.Error("the hash is not stable")
	}
}

func TestPromptDigestsReadTheFilesAndFailForAMissingOne(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("be brief"), 0o644)
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Prompt = "p.md"
	project.Agents["coder"] = coder
	digests, err := PromptDigests(project, dir)
	if err != nil || len(digests["coder"]) != 64 || digests["review"] != "" {
		t.Fatalf("digests = %v, err = %v", digests, err)
	}
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("be verbose"), 0o644)
	again, _ := PromptDigests(project, dir)
	if again["coder"] == digests["coder"] {
		t.Error("the digest ignores the content")
	}
	os.Remove(filepath.Join(dir, "p.md"))
	if _, err := PromptDigests(project, dir); err == nil {
		t.Error("a missing prompt file was not reported")
	}
}

// --- R-28: pulling images --------------------------------------------------------------------------------------------

func TestAPullStreamThatReportsAnErrorIsAnError(t *testing.T) {
	ok := `{"status":"Pulling from library/alpine"}` + "\n" + `{"status":"Download complete","id":"abc"}` + "\n"
	if err := checkPullStream(strings.NewReader(ok)); err != nil {
		t.Errorf("a good pull: %v", err)
	}
	failed := ok + `{"errorDetail":{"message":"manifest unknown"},"error":"manifest for x:1 not found: manifest unknown"}` + "\n"
	if err := checkPullStream(strings.NewReader(failed)); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("a failed pull was reported as %v", err)
	}
	if err := checkPullStream(strings.NewReader("not json\n")); err == nil {
		t.Error("garbage was accepted as a finished pull")
	}
}

func TestRegistryCredentialsComeFromTheDockerConfig(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	config := []byte(`{"auths":{"ghcr.io":{"auth":"` + auth + `"},"https://index.docker.io/v1/":{"auth":"` + auth + `"}}}`)
	cases := map[string]bool{
		"ghcr.io/egzo-ai/egzo:1": true, "alpine:3": true, "docker.io/library/alpine:3": true, "quay.io/x/y:1": false, "localhost:5000/x:1": false,
	}
	for ref, wantAuth := range cases {
		got := registryAuth(ref, config)
		if (got != "") != wantAuth {
			t.Errorf("%s: auth present = %v, want %v", ref, got != "", wantAuth)
			continue
		}
		if got != "" {
			raw, err := base64.URLEncoding.DecodeString(got)
			var decoded struct{ Username, Password string }
			if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Username != "alice" || decoded.Password != "s3cret" {
				t.Errorf("%s: auth = %s (%v)", ref, raw, err)
			}
		}
	}
	if registryAuth("ghcr.io/x:1", []byte("not json")) != "" {
		t.Error("a broken config produced credentials")
	}
}

// --- R-25: what down --workspaces may remove ------------------------------------------------------------------------------

func TestOnlyRealCheckoutsAwayFromTheProjectAreRemovable(t *testing.T) {
	project := t.TempDir()
	checkout := filepath.Join(project, ".egzo", "workspaces", "repo", "coder")
	os.MkdirAll(filepath.Join(checkout, ".git"), 0o755)
	plain := filepath.Join(project, "docs")
	os.MkdirAll(plain, 0o755)
	home, _ := os.UserHomeDir()
	for dir, want := range map[string]bool{checkout: true, plain: false, project: false, filepath.Dir(project): false, "/": false, home: false} {
		err := RemovalProblem(dir, project)
		if (err == nil) != want {
			t.Errorf("RemovalProblem(%s) = %v, removable = %v", dir, err, want)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(checkout, link)
	if RemovalProblem(link, project) == nil {
		t.Error("a symlink to a checkout is removable")
	}
}

func TestACheckoutInsideTheProjectDirectoryIsStillRemovable(t *testing.T) {
	project := t.TempDir()
	checkout := filepath.Join(project, "coder")
	os.MkdirAll(filepath.Join(checkout, ".git"), 0o755)
	if err := RemovalProblem(checkout, project); err != nil {
		t.Errorf("with `path: .` the checkouts live next to egzo.yaml: %v", err)
	}
}

// --- R-26: reading what a checkout holds -----------------------------------------------------------------------------------

func TestParseInspection(t *testing.T) {
	good := "/check uncommitted=2 unpushed=1 stashes=3 ignored=4\n"
	u, err := parseInspection("/check", good)
	if err != nil || u.Uncommitted != 2 || u.Unpushed != 1 || u.Stashes != 3 || u.Ignored != 4 {
		t.Fatalf("parse = %+v, %v", u, err)
	}
	for name, text := range map[string]string{
		"empty":           "",
		"another target":  "/check/9 uncommitted=0 unpushed=0 stashes=0 ignored=0\n",
		"two lines":       good + good,
		"missing a field": "/check uncommitted=2 unpushed=1\n",
		"negative":        "/check uncommitted=-1 unpushed=0 stashes=0 ignored=0\n",
	} {
		if _, err := parseInspection("/check", text); err == nil {
			t.Errorf("%s: accepted, but an inspection that cannot be read must fail closed", name)
		}
	}
	if _, err := parseInspection("/check", "warning: unable to access x\n"+good); err != nil {
		t.Errorf("a warning line before the answer is harmless: %v", err)
	}
}

func TestUnsavedWorkBlocksButIgnoredFilesOnlyInform(t *testing.T) {
	if !(Unsaved{Stashes: 1}).Blocks() || !(Unsaved{Uncommitted: 1}).Blocks() || !(Unsaved{Unpushed: 1}).Blocks() {
		t.Error("unsaved work must block removal")
	}
	if (Unsaved{Ignored: 5}).Blocks() {
		t.Error("ignored files alone must not block removal")
	}
	text := Unsaved{Dir: "/d", Stashes: 2, Uncommitted: 1}.String()
	if !strings.Contains(text, "stash") || !strings.Contains(text, "uncommitted") {
		t.Errorf("String = %q", text)
	}
}

// --- R-06: nothing an agent wrote reaches the terminal as a control sequence ----------------------------------------

func TestPsShowsAnAgentsStatusAsOneHarmlessLine(t *testing.T) {
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-coder-1", Service: "coder", Kind: kindAgent, State: "running"},
		{Type: "container", Name: "p-proxy-1", Service: "proxy", Kind: kindProxy, State: "running"},
	}}
	var out bytes.Buffer
	WriteStatus(observed, map[string]Report{"coder": {Status: "hi\n\x1b]52;c;ZXZpbA==\x07p-fake-1  fake  running\x1b[2J", Activity: "idle"}}, &out)
	if strings.ContainsAny(out.String(), "\x1b\x07\x00") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 {
		t.Errorf("a status line forged rows: %q", out.String())
	}
}

func TestOnlyOneCommandChangesAProjectAtATime(t *testing.T) {
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil || !strings.Contains(err.Error(), "another egzo command") {
		t.Fatalf("a second lock on the same project: %v", err)
	}
	other := t.TempDir()
	if releaseOther, err := Lock(other); err != nil {
		t.Errorf("another project was blocked: %v", err)
	} else {
		releaseOther()
	}
	release()
	again, err := Lock(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}
