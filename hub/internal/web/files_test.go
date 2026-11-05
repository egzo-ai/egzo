package web_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/internal/files"
)

// filesRig links a project whose workspace docs holds a page an agent wrote, and whose git workspace repo has the worktree of a removed
// agent. The file servers are core's own, on directories of the test; seen keeps the headers they were sent.
func filesRig(t *testing.T) (*rig, *browser, *headersSeen) {
	t.Helper()
	r := newRig(t)
	f := r.link("web", "coder")
	docs, worktree := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(docs, "notes"), 0o755)
	os.WriteFile(filepath.Join(docs, "notes", "a.txt"), []byte("alpha\n"), 0o644)
	os.WriteFile(filepath.Join(docs, "page.html"), []byte("<script>alert(1)</script>"), 0o644)
	os.WriteFile(filepath.Join(worktree, "wip.txt"), []byte("not committed\n"), 0o644)
	seen := &headersSeen{}
	for _, served := range []struct{ workspace, instance, dir string }{{"docs", "", docs}, {"repo", "gone", worktree}} {
		root, err := os.OpenRoot(served.dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		f.ServeFiles(served.workspace, served.instance, seen.wrap(files.Handler{Root: root}))
	}
	f.SetWorkspaces(
		links.Workspace{Name: "docs", Kind: "directory", Worktrees: []links.Worktree{}},
		links.Workspace{Name: "repo", Kind: "git", Worktrees: []links.Worktree{{Instance: "gone", State: "removed", Actor: rbac.Actor(r.userID("admin"))}}},
	)
	b := r.browser()
	b.signIn("admin")
	return r, b, seen
}

type headersSeen struct {
	mu   sync.Mutex
	last http.Header
}

func (h *headersSeen) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.last = r.Header.Clone()
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// raw fetches an API address as a download link would, with headers of its own.
func (b *browser) raw(method, path string, header ...string) (*http.Response, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+"/api/v1"+path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func TestWorkspacesAreListedWithTheWorktreesAgentsLeftAndWhoMadeThem(t *testing.T) {
	r, b, _ := filesRig(t)
	code, v := b.get("/p/" + r.id("web") + "/workspaces")
	listed := list(v, "workspaces")
	if code != 200 || len(listed) != 2 || listed[0]["name"] != "docs" || listed[1]["kind"] != "git" {
		t.Fatalf("%d %v", code, v)
	}
	worktree := listed[1]["worktrees"].([]any)[0].(map[string]any)
	if worktree["instance"] != "gone" || worktree["state"] != "removed" || worktree["creator"] != "admin" {
		t.Errorf("worktree = %v", worktree)
	}
}

func TestADirectoryIsAListingAndAFileIsADownloadThatRunsNothing(t *testing.T) {
	r, b, _ := filesRig(t)
	base := "/p/" + r.id("web") + "/files/docs/"
	code, v := b.get(base)
	if code != 200 || len(list(v, "entries")) != 2 || list(v, "entries")[0]["name"] != "notes" {
		t.Fatalf("listing: %d %v", code, v)
	}
	resp, body := b.raw("GET", base+"page.html")
	if resp.StatusCode != 200 || body != "<script>alert(1)</script>" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	for name, want := range map[string]string{
		"Content-Type": "application/octet-stream", "Content-Disposition": `attachment; filename=page.html`,
		"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox; default-src 'none'",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	resp, body = b.raw("GET", base+"notes/a.txt", "Range", "bytes=1-3")
	if resp.StatusCode != http.StatusPartialContent || body != "lph" {
		t.Errorf("range: %d %q", resp.StatusCode, body)
	}
	if code, v := b.get("/p/" + r.id("web") + "/worktrees/repo/gone/"); code != 200 || list(v, "entries")[0]["name"] != "wip.txt" {
		t.Errorf("a removed agent's worktree: %d %v", code, v)
	}
}

func TestOnlyWhatTheFileServerNeedsReachesIt(t *testing.T) {
	r, b, seen := filesRig(t)
	b.raw("GET", "/p/"+r.id("web")+"/files/docs/notes/a.txt", "Range", "bytes=0-1", "X-Secret", "no", "Authorization", "Bearer no")
	seen.mu.Lock()
	defer seen.mu.Unlock()
	for name := range seen.last {
		if name != "Range" && name != "Accept-Encoding" && name != "User-Agent" {
			t.Errorf("the file server was sent %s", name)
		}
	}
	if seen.last.Get("Range") != "bytes=0-1" || seen.last.Get("Cookie") != "" {
		t.Errorf("headers = %v", seen.last)
	}
}

func TestWhatCannotBeReadSaysSoInTheHubsWords(t *testing.T) {
	r, b, _ := filesRig(t)
	base := "/p/" + r.id("web")
	for path, want := range map[string]int{
		base + "/files/docs/nothing":    404,
		base + "/files/ghost/":          404,
		base + "/files/..%2fetc/":       404,
		base + "/worktrees/repo/other/": 404,
	} {
		code, v := b.get(path)
		if code != want || v["message"] == nil {
			t.Errorf("%s = %d %v", path, code, v)
		}
	}
	for _, method := range []string{"DELETE", "PUT", "POST"} {
		if code, _ := b.call(method, base+"/files/docs/page.html", nil); code < 400 {
			t.Errorf("%s = %d", method, code)
		}
	}
	if resp, body := b.raw("GET", base+"/files/docs/page.html"); resp.StatusCode != 200 || body != "<script>alert(1)</script>" {
		t.Errorf("the file changed: %d %q", resp.StatusCode, body)
	}
}

func TestReadingFilesTakesARoleAboveViewerAndADownloadIsAudited(t *testing.T) {
	r, admin, _ := filesRig(t)
	alice := r.browser()
	alice.signIn("alice")
	path := "/p/" + r.id("web") + "/files/docs/page.html"
	if code, _ := alice.get("/p/" + r.id("web") + "/workspaces"); code != 404 {
		t.Errorf("no role: %d", code)
	}
	r.store.SetRole(ctx, r.id("web"), r.userID("alice"), rbac.Viewer)
	if resp, _ := alice.raw("GET", path); resp.StatusCode != 404 {
		t.Errorf("a viewer: %d", resp.StatusCode)
	}
	r.store.SetRole(ctx, r.id("web"), r.userID("alice"), rbac.User)
	if resp, _ := alice.raw("GET", path); resp.StatusCode != 200 {
		t.Errorf("a user: %d", resp.StatusCode)
	}
	_, v := admin.get("/p/" + r.id("web") + "/audit")
	found := false
	for _, e := range list(v, "entries") {
		if e["action"] == "files.download" && e["actor"] == "alice" && strings.Contains(e["detail"].(string), "page.html") {
			found = true
		}
	}
	if !found {
		t.Errorf("no download in the audit log: %v", v)
	}
}
