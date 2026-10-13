package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/oidctest"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/web"
)

type rig struct {
	t        *testing.T
	server   *httptest.Server
	provider *httptest.Server
	hub      *hub.Hub
	store    *store.Store
	fakes    map[string]*fake.Engine
}

var ctx = context.Background()

func newRig(t *testing.T, engineNames ...string) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := secretbox.New("0123456789abcdef-secret")
	r := &rig{t: t, store: s, fakes: map[string]*fake.Engine{}}
	registry := engines.NewRegistry(s, box, func(_ context.Context, cfg engines.Config) (engines.Engine, error) {
		f, ok := r.fakes[cfg.Name]
		if !ok {
			f = fake.New()
			r.fakes[cfg.Name] = f
		}
		return f, nil
	})
	registry.SetProbe(func(context.Context, engines.Config) (string, error) { return "SHA256:probed-key", nil })
	r.hub = hub.New(s, registry, nil)
	for _, name := range engineNames {
		r.fakes[name] = fake.New()
		s.PutEngine(ctx, store.EngineRow{Name: name, Host: "unix:///" + name})
	}
	provider := oidctest.New("", "hub", "secret")
	r.provider = httptest.NewServer(provider.Handler())
	provider.Issuer = r.provider.URL
	t.Cleanup(r.provider.Close)
	urls, _ := publicurl.New("", nil)
	app, err := web.New(r.hub, auth.New(auth.Config{Issuer: r.provider.URL, ClientID: "hub", ClientSecret: "secret"}), urls, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.server = httptest.NewServer(app.Handler())
	t.Cleanup(r.server.Close)
	return r
}

// browser is an http client with cookies, as a person's browser; it sends the Origin header a real one sends on a form.
type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func (r *rig) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: r.t, base: r.server.URL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b *browser) do(method, path string, form url.Values) (int, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == "POST" {
		req.Header.Set("Origin", b.base)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (b *browser) get(path string) (int, string)                { return b.do("GET", path, nil) }
func (b *browser) post(path string, f url.Values) (int, string) { return b.do("POST", path, f) }

// signIn walks the login: the hub redirects to the provider, the person types a name, the provider redirects back.
func (b *browser) signIn(user string) {
	b.t.Helper()
	noFollow := *b.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Get(b.base + "/login/start")
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("login start = %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	q := authURL.Query()
	resp, err = noFollow.PostForm(authURL.Scheme+"://"+authURL.Host+"/authorize", url.Values{
		"user": {user}, "client_id": q["client_id"], "redirect_uri": q["redirect_uri"], "state": q["state"], "nonce": q["nonce"],
	})
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	resp, err = b.client.Get(b.base + back.RequestURI())
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("callback ended on %d", resp.StatusCode)
	}
}

func (r *rig) userID(name string) string {
	r.t.Helper()
	users, _ := r.store.Users(ctx)
	for _, u := range users {
		if u.Name == name {
			return u.ID
		}
	}
	r.t.Fatalf("no user %s", name)
	return ""
}

func project(name string, templates ...string) engines.Project {
	return engines.Project{Name: name, Templates: templates}
}

func agent(name, actor string) engines.Agent {
	return engines.Agent{Name: name, Template: "coder", State: "running", Activity: "idle", Actor: actor, Status: "reading the code", Created: time.Now()}
}

func TestAnonymousVisitorsAreSentToTheLogin(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := b.client.Get(b.base + "/e/local/p/web")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Errorf("status %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := (&browser{t: t, base: b.base, client: &http.Client{}}).get("/healthz"); code != 200 {
		t.Errorf("healthz = %d", code)
	}
	for _, path := range []string{"/e/local/p/web/a/x/terminal?fragment=1", "/?fragment=1"} {
		resp, _ := b.client.Get(b.base + path)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401 for a fragment", path, resp.StatusCode)
		}
	}
}

func TestSigningInMakesTheFirstUserTheOperatorWhoOwnsTheOrphans(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	code, page := b.get("/")
	if code != 200 || !strings.Contains(page, `href="/e/local/p/web"`) || !strings.Contains(page, "operator") {
		t.Fatalf("home = %d\n%s", code, page)
	}
	if strings.Contains(page, "engine-badge") {
		t.Error("the engine badge is shown with one engine")
	}
	if !strings.Contains(page, "root") {
		t.Error("the signed-in user is not shown")
	}
}

func TestANewcomerSeesNothingAndIsToldWhy(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	r.browser().signIn("root")
	b := r.browser()
	b.signIn("alice")
	_, page := b.get("/")
	if strings.Contains(page, `/e/local/p/web`) || !strings.Contains(page, "no role on any project") {
		t.Errorf("home for a newcomer:\n%s", page)
	}
	if code, _ := b.get("/e/local/p/web"); code != 404 {
		t.Errorf("a project without a role = %d, want 404", code)
	}
	if code, _ := b.get("/engines"); code != 403 {
		t.Errorf("the engines page for a non-operator = %d", code)
	}
}

func TestEngineBadgesAndTheFilterAppearWithSeveralEngines(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["local"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("data"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	_, page := b.get("/")
	for _, want := range []string{`engine-badge">local`, `engine-badge">lab`, `href="/?engine=lab"`, `href="/e/lab/p/data"`, `href="/e/local/p/web"`, `href="/e/lab/p/web"`} {
		if !strings.Contains(page, want) {
			t.Errorf("home lacks %q", want)
		}
	}
	_, filtered := b.get("/?engine=lab")
	if strings.Contains(filtered, `href="/e/local/p/web"`) || !strings.Contains(filtered, `href="/e/lab/p/data"`) {
		t.Error("the engine filter does not filter")
	}
	r.fakes["lab"].SetDown(fake.ErrUnreachable)
	r.hub.Scan(ctx)
	_, page = b.get("/")
	if !strings.Contains(page, "lab</strong> is unreachable") || !strings.Contains(page, `href="/e/local/p/web"`) {
		t.Errorf("an engine down breaks the page:\n%s", page)
	}
}

func TestAProjectPageOffersOnlyWhatTheRoleAllows(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("root")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.User)

	_, page := root.get("/e/local/p/web")
	for _, want := range []string{"Stop", "Remove", "Spawn an agent", "Members", "Audit log", "reading the code", "operator (CLI)"} {
		if !strings.Contains(page, want) {
			t.Errorf("owner page lacks %q", want)
		}
	}
	_, page = alice.get("/e/local/p/web")
	if strings.Contains(page, ">Stop<") || strings.Contains(page, ">Remove<") || strings.Contains(page, "Members") || strings.Contains(page, "Audit log") {
		t.Errorf("a user is offered what they cannot do:\n%s", page)
	}
	if !strings.Contains(page, "Spawn an agent") {
		t.Error("a user cannot spawn")
	}
	if code, _ := alice.post("/e/local/p/web/a/coder-1/stop", url.Values{}); code != 403 {
		t.Errorf("a user stopping an agent of the operator = %d", code)
	}
	if code, _ := alice.get("/e/local/p/web/members"); code != 403 {
		t.Errorf("members for a user = %d", code)
	}
	if code, _ := alice.get("/e/local/p/web/audit"); code != 403 {
		t.Errorf("audit for a user = %d", code)
	}
}

func TestSpawnStopStartAndRemoveThroughThePages(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	uid := r.userID("root")

	code, page := b.post("/e/local/p/web/spawn", url.Values{"template": {"coder"}, "name": {"fix-bug"}})
	if code != 200 || !strings.Contains(page, "Spawned fix-bug") || !strings.Contains(page, "<h1>fix-bug</h1>") {
		t.Fatalf("spawn = %d\n%s", code, page)
	}
	if calls := r.fakes["local"].Calls(); calls[0] != "spawn web/fix-bug user:"+uid {
		t.Errorf("calls = %v", calls)
	}
	if _, page = b.post("/e/local/p/web/a/fix-bug/stop", url.Values{}); !strings.Contains(page, "Stopped fix-bug") || !strings.Contains(page, ">Start<") {
		t.Errorf("after stop:\n%s", page)
	}
	if _, page = b.post("/e/local/p/web/a/fix-bug/start", url.Values{}); !strings.Contains(page, "Started fix-bug") || !strings.Contains(page, ">Stop<") {
		t.Errorf("after start:\n%s", page)
	}
	code, page = b.post("/e/local/p/web/a/fix-bug/rm", url.Values{})
	if code != 200 || !strings.Contains(page, "Removed fix-bug") || strings.Contains(page, `data-agent="fix-bug"`) {
		t.Errorf("after rm = %d\n%s", code, page)
	}
	if _, page = b.post("/e/local/p/web/spawn", url.Values{"template": {"ghost"}}); !strings.Contains(page, "Could not spawn") {
		t.Errorf("a spawn that fails says nothing:\n%s", page)
	}
	if code, _ := b.post("/e/local/p/web/a/x/explode", url.Values{}); code != 404 {
		t.Errorf("an unknown verb = %d", code)
	}
}

func TestStatePageShowsAWaitingAgentAndTheTimelineWithoutMessageTexts(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := agent("coder-1", "operator")
	a.Waiting = true
	r.fakes["local"].SetAgent("web", a)
	r.fakes["local"].AddEvent("web", engines.Event{Seq: 1, Time: time.Now(), Type: "status", Agent: "coder-1", Actor: "agent:coder-1", Text: "parsing the grammar"})
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	_, page := b.get("/e/local/p/web/a/coder-1")
	for _, want := range []string{"waiting for an answer", "parsing the grammar", "reading the code", "Open terminal"} {
		if !strings.Contains(page, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}
	if strings.Contains(page, "<textarea") || strings.Contains(page, "Send a message") {
		t.Error("the web UI has a messaging box")
	}
	_, fragment := b.get("/e/local/p/web/a/coder-1?fragment=1")
	if strings.Contains(fragment, "<html") || !strings.Contains(fragment, "waiting for an answer") {
		t.Errorf("the fragment is a whole page or lacks the status:\n%s", fragment)
	}
}

func TestAStoppedAgentHasNoTerminalButAStartButton(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := agent("coder-1", "operator")
	a.State, a.Activity = "exited", "stopped"
	r.fakes["local"].SetAgent("web", a)
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	_, page := b.get("/e/local/p/web/a/coder-1")
	if strings.Contains(page, "Open terminal") || !strings.Contains(page, ">Start<") {
		t.Errorf("stopped agent page:\n%s", page)
	}
}

func TestStateChangingRequestsNeedTheOrigin(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	for _, origin := range []string{"", "https://evil.example"} {
		req, _ := http.NewRequest("POST", b.base+"/e/local/p/web/spawn", strings.NewReader("template=coder"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, _ := b.client.Do(req)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q = %d, want 403", origin, resp.StatusCode)
		}
	}
	if calls := r.fakes["local"].Calls(); len(calls) != 0 {
		t.Errorf("a refused request did something: %v", calls)
	}
}

func TestMembersCanBeAddedChangedAndRemovedByAnOwner(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("root")
	alice := r.browser()
	alice.signIn("alice")
	uid := r.userID("alice")

	_, page := root.get("/e/local/p/web/members")
	if !strings.Contains(page, "alice@example.com") || !strings.Contains(page, "Add a person") {
		t.Fatalf("members page:\n%s", page)
	}
	if _, page = root.post("/e/local/p/web/members", url.Values{"user": {uid}, "role": {"administrator"}}); !strings.Contains(page, "Roles updated") {
		t.Errorf("grant:\n%s", page)
	}
	if role, _ := r.store.Role(ctx, "local", "web", uid); role != rbac.Administrator {
		t.Errorf("role = %q", role)
	}
	if code, _ := alice.get("/e/local/p/web/audit"); code != 200 {
		t.Errorf("a new administrator cannot read the audit log: %d", code)
	}
	if code, _ := alice.post("/e/local/p/web/members", url.Values{"user": {uid}, "role": {"owner"}}); code != 403 {
		t.Errorf("an administrator granting owner = %d", code)
	}
	if _, page = root.post("/e/local/p/web/members", url.Values{"user": {r.userID("root")}, "role": {"none"}}); !strings.Contains(page, "cannot remove or lower themselves") {
		t.Errorf("an owner removing themselves:\n%s", page)
	}
	root.post("/e/local/p/web/members", url.Values{"user": {uid}, "role": {"none"}})
	if code, _ := alice.get("/e/local/p/web"); code != 404 {
		t.Errorf("a removed member still sees the project: %d", code)
	}
}

func TestTheOperatorAddsAnEngineAndAnSSHHostKeyMustBeConfirmed(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	b.signIn("root")
	if _, page := b.post("/engines", url.Values{"name": {"lab"}, "host": {"unix:///lab.sock"}}); !strings.Contains(page, "Saved engine lab") || !strings.Contains(page, "<td>lab</td>") {
		t.Fatalf("add engine:\n%s", page)
	}
	code, page := b.post("/engines", url.Values{"name": {"far"}, "host": {"ssh://me@far"}, "ssh_key": {"SECRET-PRIVATE-KEY"}})
	if code != 200 || !strings.Contains(page, "SHA256:probed-key") || !strings.Contains(page, "Pin this key and save") {
		t.Fatalf("host key page = %d\n%s", code, page)
	}
	if strings.Contains(page, "SECRET-PRIVATE-KEY") {
		t.Error("the private key was sent back to the browser")
	}
	if rows, _ := r.store.Engines(ctx); len(rows) != 2 {
		t.Errorf("an engine was saved before its host key was confirmed: %+v", rows)
	}
	token := between(page, `name="token" value="`, `"`)
	if _, page = b.post("/engines/confirm", url.Values{"token": {token}}); !strings.Contains(page, "Saved engine far") {
		t.Fatalf("confirm:\n%s", page)
	}
	row, _ := r.store.Engine(ctx, "far")
	if row.HostKey != "SHA256:probed-key" {
		t.Errorf("pinned = %q", row.HostKey)
	}
	if _, page = b.post("/engines/confirm", url.Values{"token": {token}}); !strings.Contains(page, "expired") {
		t.Errorf("a token can be used twice:\n%s", page)
	}
	if code, page = b.post("/engines", url.Values{"name": {"Bad Name"}, "host": {"unix:///x"}}); code != 422 || !strings.Contains(page, "lowercase") {
		t.Errorf("a bad name = %d\n%s", code, page)
	}
	if _, page = b.post("/engines/lab/remove", url.Values{}); !strings.Contains(page, "Removed engine lab") {
		t.Errorf("remove:\n%s", page)
	}
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	return s[:strings.Index(s, to)]
}

func TestLogoutEndsTheSession(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	b.signIn("root")
	if code, _ := b.get("/"); code != 200 {
		t.Fatal(code)
	}
	b.post("/logout", url.Values{})
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := b.client.Get(b.base + "/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout = %d", resp.StatusCode)
	}
}

func TestTheLoginNextAddressStaysOnTheHub(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example"} {
		_, page := b.get("/login?next=" + url.QueryEscape(next))
		if strings.Contains(page, "evil.example") {
			t.Errorf("next=%q is reflected into the login link", next)
		}
	}
}

func (b *browser) dial(path string) (*websocket.Conn, error) {
	u, _ := url.Parse(b.base)
	var cookies []string
	for _, c := range b.client.Jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(b.base, "http")+path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {strings.Join(cookies, "; ")}, "Origin": {b.base}},
	})
	return conn, err
}

func readControl(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	kind, data, err := conn.Read(c)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("control frame: %v %v", kind, err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	return m
}

func readUntil(t *testing.T, conn *websocket.Conn, want string) string {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		kind, data, err := conn.Read(c)
		if err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, got.String(), err)
		}
		if kind == websocket.MessageBinary {
			got.Write(data)
			if strings.Contains(got.String(), want) {
				return got.String()
			}
		}
	}
}

func TestTheTerminalBridgesTheBrowserToTheAgent(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("root")
	path := "/e/local/p/web/a/coder-1/terminal"

	writer, err := root.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.CloseNow()
	if m := readControl(t, writer); m["type"] != "mode" || m["readOnly"] == true {
		t.Errorf("the first to attach should type: %v", m)
	}
	writer.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	readUntil(t, writer, "attached to web/coder-1 (100x30)")
	writer.Write(ctx, websocket.MessageBinary, []byte("hello\r"))
	readUntil(t, writer, "you said: hello")
	writer.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	readUntil(t, writer, "resized to 120x40")

	// the page shows who is at the terminal
	if _, page := root.get("/e/local/p/web/a/coder-1"); !strings.Contains(page, "root (typing)") {
		t.Errorf("presence missing:\n%s", page)
	}

	// a second person watches
	r.store.SetRole(ctx, "local", "web", r.userID("root"), rbac.Owner)
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.User)
	watcher, err := alice.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.CloseNow()
	if m := readControl(t, watcher); m["readOnly"] != true {
		t.Errorf("the second terminal should only watch: %v", m)
	}
	watcher.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`))
	readUntil(t, watcher, "attached to")
	watcher.Write(ctx, websocket.MessageBinary, []byte("sneaky\r"))
	time.Sleep(200 * time.Millisecond)
	writer.Write(ctx, websocket.MessageBinary, []byte("exit\r"))
	if _, data, err := func() (websocket.MessageType, []byte, error) {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for {
			k, d, err := writer.Read(c)
			if err != nil {
				return k, d, err
			}
		}
	}(); err == nil || len(data) > 0 {
		_ = data
	}
	for _, line := range r.fakes["local"].Calls() {
		if strings.Contains(line, "sneaky") {
			t.Error("a watcher typed")
		}
	}
}

func TestTheTerminalRefusesWhatThePagesRefuse(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	r.browser().signIn("root")
	eve := r.browser()
	eve.signIn("eve")
	if _, err := eve.dial("/e/local/p/web/a/coder-1/terminal"); err == nil {
		t.Error("a person without a role opened a terminal")
	}
	anon := r.browser()
	if _, err := anon.dial("/e/local/p/web/a/coder-1/terminal"); err == nil {
		t.Error("an anonymous visitor opened a terminal")
	}
	// another origin is refused even with a valid cookie
	root := r.browser()
	root.signIn("root")
	u, _ := url.Parse(root.base)
	var cookies []string
	for _, c := range root.client.Jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	_, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(root.base, "http")+"/e/local/p/web/a/coder-1/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {strings.Join(cookies, "; ")}, "Origin": {"https://evil.example"}},
	})
	if err == nil {
		t.Error("a cross-origin websocket was accepted")
	}
}

func TestSpecAndLogsPagesShowReadOnlyText(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("root")
	if _, page := b.get("/e/local/p/web/spec"); !strings.Contains(page, "name: web") {
		t.Errorf("spec:\n%s", page)
	}
	if _, page := b.get("/e/local/p/web/a/coder-1/logs"); !strings.Contains(page, "log of coder-1") {
		t.Errorf("logs:\n%s", page)
	}
}

func TestAuditPageListsRefusals(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("root")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.User)
	alice.post("/e/local/p/web/a/coder-1/rm", url.Values{})
	if _, page := root.get("/e/local/p/web/audit"); !strings.Contains(page, "agent.rm") || !strings.Contains(page, "refused") || !strings.Contains(page, "alice") {
		t.Errorf("audit:\n%s", page)
	}
}
