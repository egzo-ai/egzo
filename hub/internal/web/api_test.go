package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestTheAppIsServedForItsOwnAddressesAndTheAPIAnswersJSONWhenSignedOut(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	for _, path := range []string{"/", "/login", "/engines", "/e/local/p/web", "/e/local/p/web/a/coder-1/logs"} {
		code, page := b.page(path)
		if code != 200 || !strings.Contains(page, "<title>egzo hub</title>") {
			t.Errorf("%s = %d %q, want the app", path, code, page)
		}
	}
	if code, page := b.page("/assets/app-1234.js"); code != 200 || page != "//js" {
		t.Errorf("an asset = %d %q", code, page)
	}
	if code, _ := b.page("/healthz"); code != 200 {
		t.Errorf("healthz = %d", code)
	}
	for _, path := range []string{"/session", "/projects", "/e/local/p/web", "/e/local/p/web/a/x/terminal", "/engines"} {
		code, body := b.get(path)
		if code != http.StatusUnauthorized || body["code"] != "unauthorized" {
			t.Errorf("%s signed out = %d %v, want a JSON 401", path, code, body)
		}
	}
	if code, body := b.get("/nothing-here"); code != 404 || body["code"] != "not_found" {
		t.Errorf("an unknown API call = %d %v", code, body)
	}
}

func TestTheStarterAccountIsAnOperatorWhoHoldsEveryProject(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	code, session := b.get("/session")
	if code != 200 || at(session, "user", "username") != "admin" || at(session, "user", "isOperator") != true {
		t.Fatalf("session = %d %v", code, session)
	}
	if got := list(session, "engines"); len(got) != 1 || got[0]["name"] != "local" || got[0]["host"] != nil {
		t.Errorf("the session lists engines (without their address): %v", got)
	}
	_, projects := b.get("/projects")
	got := list(projects, "projects")
	if len(got) != 1 || got[0]["name"] != "web" || got[0]["engine"] != "local" || got[0]["role"] != "owner" || got[0]["operator"] != true {
		t.Errorf("projects = %v", projects)
	}
	if grants, _ := r.store.GrantsOf(ctx, r.userID("admin")); len(grants) != 0 {
		t.Errorf("an operator was given grants: %+v", grants)
	}
}

func TestANewcomerSeesNothing(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	r.browser().signIn("admin")
	b := r.browser()
	b.signIn("alice")
	_, projects := b.get("/projects")
	if got := list(projects, "projects"); len(got) != 0 {
		t.Errorf("a newcomer sees %v", got)
	}
	if code, _ := b.get("/e/local/p/web"); code != 404 {
		t.Errorf("a project without a role = %d, want 404", code)
	}
	if code, _ := b.get("/engines"); code != 403 {
		t.Errorf("the engines for a non-operator = %d", code)
	}
}

func TestProjectsComeFromEveryEngineAndAnEngineDownIsReportedWithoutLosingTheOthers(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["local"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("data"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	_, projects := b.get("/projects")
	var keys []string
	for _, p := range list(projects, "projects") {
		keys = append(keys, p["engine"].(string)+"/"+p["name"].(string))
	}
	if strings.Join(keys, " ") != "lab/data lab/web local/web" {
		t.Errorf("projects = %v", keys)
	}
	r.fakes["lab"].SetDown(fake.ErrUnreachable)
	r.hub.Scan(ctx)
	_, projects = b.get("/projects")
	down := list(projects, "unreachable")
	if len(down) != 1 || down[0]["name"] != "lab" || down[0]["err"] == "" {
		t.Errorf("unreachable = %v", down)
	}
	if got := list(projects, "projects"); len(got) != 1 || got[0]["engine"] != "local" {
		t.Errorf("an engine down breaks the list: %v", got)
	}
}

func TestAProjectOffersOnlyWhatTheRoleAllows(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.User)

	_, owner := root.get("/e/local/p/web")
	if owner["canSpawn"] != true || owner["canManage"] != true || owner["canAudit"] != true || owner["role"] != "owner" {
		t.Errorf("owner view = %v", owner)
	}
	a := list(owner, "agents")[0]
	if a["canOperate"] != true || a["status"] != "reading the code" || a["creator"] != "operator (CLI)" {
		t.Errorf("owner's agent = %v", a)
	}
	_, user := alice.get("/e/local/p/web")
	if user["canSpawn"] != true || user["canManage"] != false || user["canAudit"] != false {
		t.Errorf("user view = %v", user)
	}
	if list(user, "agents")[0]["canOperate"] != false {
		t.Errorf("a user may operate the operator's agent: %v", user)
	}
	if code, _ := alice.post("/e/local/p/web/a/coder-1/stop", nil); code != 403 {
		t.Errorf("a user stopping an agent of the operator = %d", code)
	}
	for _, path := range []string{"/members", "/audit"} {
		if code, _ := alice.get("/e/local/p/web" + path); code != 403 {
			t.Errorf("%s for a user = %d", path, code)
		}
	}
}

func TestTheAgentsActivityComesFromTheStatusFeedAndTheFeedIsReported(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	working := agent("coder-1", "operator")
	working.Activity, working.Waiting = "working", true
	r.fakes["local"].SetAgent("web", working)
	r.fakes["local"].AddProject(project("mute", "coder"))
	r.fakes["local"].SetAgent("mute", agent("coder-1", "operator"))
	r.fakes["local"].SetFeedSilent("mute", true) // its control sidecar logs no status
	feeds, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	r.hub.StartFeeds(feeds, hub.FeedOptions{Silent: 150 * time.Millisecond, BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")

	var web, mute map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, projects := b.get("/projects")
		for _, p := range list(projects, "projects") {
			if p["name"] == "web" {
				web = p
			} else {
				mute = p
			}
		}
		if at(json_(web), "feed", "state") == "live" && at(json_(mute), "feed", "state") == "unavailable" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if at(json_(web), "feed", "state") != "live" {
		t.Fatalf("web feed = %v", web["feed"])
	}
	if a := list(json_(web), "agents")[0]; a["activity"] != "working" || a["waiting"] != true {
		t.Errorf("the list shows what the feed said: %v", a)
	}
	if at(json_(mute), "feed", "state") != "unavailable" || !strings.Contains(at(json_(mute), "feed", "message").(string), "writes no status") {
		t.Fatalf("mute feed = %v", mute["feed"])
	}
	if a := list(json_(mute), "agents")[0]; a["activity"] != "unknown" || a["running"] != true {
		t.Errorf("a running agent whose status cannot be read is unknown, not starting: %v", a)
	}
	// the project page says the same
	_, page := b.get("/e/local/p/mute")
	if at(page, "feed", "state") != "unavailable" || list(page, "agents")[0]["activity"] != "unknown" {
		t.Errorf("project page = %v", page)
	}
}

func TestSpawnStopStartAndRemove(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	uid := r.userID("admin")

	// a spawn is answered at once, before the agent exists; the project lists it until it has ended, and then how it ended
	r.fakes["local"].HoldSpawns()
	code, out := b.post("/e/local/p/web/spawn", json_{"template": "coder", "name": "fix-bug"})
	started, _ := out["spawn"].(map[string]any)
	if code != http.StatusAccepted || started["state"] != "spawning" || started["id"] == "" || started["name"] != "fix-bug" || started["template"] != "coder" {
		t.Fatalf("spawn = %d %v", code, out)
	}
	spawns := func() []map[string]any {
		_, v := b.get("/e/local/p/web")
		return list(v, "spawns")
	}
	if got := spawns(); len(got) != 1 || got[0]["id"] != started["id"] || got[0]["state"] != "spawning" || got[0]["creator"] != "admin" {
		t.Errorf("while it runs: %v", got)
	}
	r.fakes["local"].ReleaseSpawns()
	r.hub.WaitForSpawns()
	if got := spawns(); len(got) != 1 || got[0]["state"] != "ready" || got[0]["agent"] != "fix-bug" || got[0]["endedAt"] == nil {
		t.Errorf("once it has ended: %v", got)
	}
	if calls := r.fakes["local"].Calls(); calls[0] != "spawn web/fix-bug user:"+uid {
		t.Errorf("calls = %v", calls)
	}
	state := func() map[string]any {
		_, v := b.get("/e/local/p/web/a/fix-bug")
		agent, _ := v["agent"].(map[string]any)
		return agent
	}
	if code, _ := b.post("/e/local/p/web/a/fix-bug/stop", nil); code != 200 || state()["running"] != false {
		t.Errorf("after stop: %v", state())
	}
	if code, _ := b.post("/e/local/p/web/a/fix-bug/start", nil); code != 200 || state()["running"] != true {
		t.Errorf("after start: %v", state())
	}
	if code, _ := b.post("/e/local/p/web/a/fix-bug/rm", nil); code != 200 {
		t.Errorf("rm = %d", code)
	}
	if code, _ := b.get("/e/local/p/web/a/fix-bug"); code != 404 {
		t.Errorf("a removed agent = %d", code)
	}
	code, out = b.post("/e/local/p/web/spawn", json_{"template": "ghost"})
	failed, _ := out["spawn"].(map[string]any)
	r.hub.WaitForSpawns()
	for _, s := range spawns() {
		if s["id"] == failed["id"] && (s["state"] != "failed" || !strings.Contains(s["error"].(string), "ghost")) {
			t.Errorf("a spawn that fails = %d %v", code, s)
		}
	}
	if code, _ := b.post("/e/local/p/web/a/x/explode", nil); code != 404 {
		t.Errorf("an unknown verb = %d", code)
	}
}

func TestAnAgentWaitingOnAQuestionAndTheTimelineWithoutMessageTexts(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := agent("coder-1", "operator")
	a.Waiting = true
	r.fakes["local"].SetAgent("web", a)
	r.fakes["local"].AddEvent("web", engines.Event{Seq: 1, Time: time.Now(), Type: "status", Agent: "coder-1", Actor: "agent:coder-1", Text: "parsing the grammar"})
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	_, v := b.get("/e/local/p/web/a/coder-1")
	got := v["agent"].(map[string]any)
	if got["waiting"] != true || got["status"] != "reading the code" || got["activity"] != "idle" {
		t.Errorf("agent = %v", got)
	}
	_, events := b.get("/e/local/p/web/events?agent=coder-1")
	rows := list(events, "events")
	if len(rows) != 1 || rows[0]["text"] != "parsing the grammar" || rows[0]["type"] != "status" {
		t.Errorf("events = %v", events)
	}
	_, projects := b.get("/projects")
	if w := list(projects, "projects")[0]["agents"].([]any)[0].(map[string]any)["waiting"]; w != true {
		t.Errorf("the project list does not say the agent is waiting: %v", projects)
	}
}

func TestStateChangingRequestsNeedTheOriginAndJSON(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	try := func(origin, contentType string) int {
		req, _ := http.NewRequest("POST", b.base+"/api/v1/e/local/p/web/spawn", strings.NewReader(`{"template":"coder"}`))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, _ := b.client.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := try("", "application/json"); got != http.StatusForbidden {
		t.Errorf("no origin = %d, want 403", got)
	}
	if got := try("https://evil.example", "application/json"); got != http.StatusForbidden {
		t.Errorf("another origin = %d, want 403", got)
	}
	if got := try(b.base, "application/x-www-form-urlencoded"); got != http.StatusUnsupportedMediaType {
		t.Errorf("a form = %d, want 415", got)
	}
	if got := try(b.base, ""); got != http.StatusUnsupportedMediaType {
		t.Errorf("no content type = %d, want 415", got)
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
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	uid := r.userID("alice")

	_, m := root.get("/e/local/p/web/members")
	if cands := list(m, "candidates"); len(cands) != 1 || cands[0]["email"] != "alice@example.com" || m["me"] != r.userID("admin") {
		t.Fatalf("members = %v", m)
	}
	// the operator is listed, as owner, marked: their access is not a role
	if rows := list(m, "members"); len(rows) != 1 || at(rows[0], "user", "username") != "admin" || rows[0]["role"] != "owner" || rows[0]["operator"] != true {
		t.Errorf("members = %v", rows)
	}
	if code, _ := root.post("/e/local/p/web/members", json_{"user": uid, "role": "administrator"}); code != 200 {
		t.Errorf("grant = %d", code)
	}
	if role, _ := r.store.Role(ctx, "local", "web", uid); role != rbac.Administrator {
		t.Errorf("role = %q", role)
	}
	if code, _ := alice.get("/e/local/p/web/audit"); code != 200 {
		t.Errorf("a new administrator cannot read the audit log: %d", code)
	}
	if code, _ := alice.post("/e/local/p/web/members", json_{"user": uid, "role": "owner"}); code != 403 {
		t.Errorf("an administrator granting owner = %d", code)
	}
	root.post("/e/local/p/web/members", json_{"user": uid, "role": "owner"})
	if code, out := alice.post("/e/local/p/web/members", json_{"user": uid, "role": "none"}); code != 403 || !strings.Contains(out["message"].(string), "cannot remove or lower themselves") {
		t.Errorf("an owner removing themselves = %d %v", code, out)
	}
	// an operator's access is not a role, whoever asks and whatever the role
	for _, who := range []*browser{root, alice} {
		for _, role := range []string{"none", "user", "owner"} {
			if code, out := who.post("/e/local/p/web/members", json_{"user": r.userID("admin"), "role": role}); code != 403 || !strings.Contains(out["message"].(string), "operator holds every project") {
				t.Errorf("setting the operator to %s = %d %v", role, code, out)
			}
		}
	}
	root.post("/e/local/p/web/members", json_{"user": uid, "role": "none"})
	if code, _ := alice.get("/e/local/p/web"); code != 404 {
		t.Errorf("a removed member still sees the project: %d", code)
	}
}

func TestOnlyAnOperatorListsUsersAndMakesOperators(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	uid := r.userID("alice")

	if code, _ := alice.get("/users"); code != 403 {
		t.Errorf("a user listing users = %d", code)
	}
	if code, _ := alice.put("/users/"+uid, json_{"isOperator": true}); code != 403 {
		t.Errorf("a user making themselves an operator = %d", code)
	}
	code, out := root.get("/users")
	rows := list(out, "users")
	if code != 200 || len(rows) != 2 || out["me"] != r.userID("admin") || rows[0]["isOperator"] != true || rows[1]["isOperator"] != false || rows[1]["createdAt"] == nil {
		t.Fatalf("users = %d %v", code, out)
	}
	if rows[1]["identity"] != nil {
		t.Errorf("the identity is shown: %v", rows[1])
	}
	if code, out := root.put("/users/"+uid, json_{}); code != 400 {
		t.Errorf("no value = %d %v", code, out)
	}
	if code, _ := root.put("/users/uGHOST", json_{"isOperator": true}); code != 404 {
		t.Errorf("a user that does not exist = %d", code)
	}
	if code, _ := root.put("/users/"+uid, json_{"isOperator": true}); code != 200 {
		t.Fatalf("making an operator = %d", code)
	}
	if code, _ := alice.get("/engines"); code != 200 {
		t.Errorf("a new operator manages engines: %d", code)
	}
	if _, projects := alice.get("/projects"); len(list(projects, "projects")) != 1 {
		t.Errorf("a new operator holds every project: %v", projects)
	}
	if code, _ := root.put("/users/"+r.userID("admin"), json_{"isOperator": false}); code != 200 {
		t.Errorf("an operator stepping down while another is one = %d", code)
	}
	if code, _ := root.get("/engines"); code != 403 {
		t.Errorf("a demoted operator still manages engines: %d", code)
	}
	if code, out := alice.put("/users/"+uid, json_{"isOperator": false}); code != 409 || out["code"] != "conflict" {
		t.Errorf("the last operator stepping down = %d %v", code, out)
	}
	if code, _ := alice.put("/users/"+uid, json_{"isOperator": true}); code != 200 {
		t.Errorf("setting the same value = %d", code)
	}
}

func TestTheOperatorAddsAnEngineAndAnSSHHostKeyMustBeConfirmed(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	b.signIn("admin")
	if code, out := b.post("/engines", json_{"name": "lab", "host": "unix:///lab.sock"}); code != 200 || out["saved"] != "lab" {
		t.Fatalf("add engine = %d %v", code, out)
	}
	code, out := b.post("/engines", json_{"name": "far", "host": "ssh://me@far", "sshKey": "SECRET-PRIVATE-KEY"})
	pending, _ := out["pending"].(map[string]any)
	if code != 200 || pending["fingerprint"] != "SHA256:probed-key" {
		t.Fatalf("host key = %d %v", code, out)
	}
	if raw, _ := json.Marshal(out); strings.Contains(string(raw), "SECRET-PRIVATE-KEY") {
		t.Error("the private key was sent back to the browser")
	}
	if rows, _ := r.store.Engines(ctx); len(rows) != 2 {
		t.Errorf("an engine was saved before its host key was confirmed: %+v", rows)
	}
	token := pending["token"]
	if code, out = b.post("/engines/confirm", json_{"token": token}); code != 200 || out["saved"] != "far" {
		t.Fatalf("confirm = %d %v", code, out)
	}
	row, _ := r.store.Engine(ctx, "far")
	if row.HostKey != "SHA256:probed-key" {
		t.Errorf("pinned = %q", row.HostKey)
	}
	if code, _ = b.post("/engines/confirm", json_{"token": token}); code != http.StatusGone {
		t.Errorf("a token can be used twice: %d", code)
	}
	if code, out = b.post("/engines", json_{"name": "Bad Name", "host": "unix:///x"}); code != 422 || !strings.Contains(out["message"].(string), "lowercase") {
		t.Errorf("a bad name = %d %v", code, out)
	}
	_, listed := b.get("/engines")
	if all := list(listed, "engines"); len(all) != 3 || all[0]["host"] == nil {
		t.Errorf("the operator lists engines with their address: %v", listed)
	}
	if code, out = b.del("/engines/lab"); code != 200 || out["removed"] != "lab" {
		t.Errorf("remove = %d %v", code, out)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	r := newRig(t, "local")
	b := r.browser()
	b.signIn("admin")
	if code, _ := b.get("/session"); code != 200 {
		t.Fatal(code)
	}
	if code, _ := b.post("/logout", nil); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	if code, _ := b.get("/session"); code != 401 {
		t.Errorf("after logout = %d", code)
	}
}

func TestALoginThatFailsReturnsToTheSignInScreenWithTheReason(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := b.client.Get(b.base + "/auth/callback?state=x")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?error=") {
		t.Errorf("a callback that did not start here = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestTheLoginNextAddressStaysOnTheHub(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// browsers drop tabs and newlines from a URL, so "/<tab>/evil.example" is "//evil.example" to them
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example", "/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/\x00/evil.example"} {
		resp, err := b.client.Get(b.base + "/login/start?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if value, _ := url.QueryUnescape(c.Value); strings.Contains(value, "evil.example") {
				t.Errorf("next=%q is kept for the redirect after the login: %q", next, value)
			}
		}
	}
}

func (b *browser) dial(path string) (*websocket.Conn, error) {
	u, _ := url.Parse(b.base)
	var cookies []string
	for _, c := range b.client.Jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(b.base, "http")+"/api/v1"+path, &websocket.DialOptions{
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
	root.signIn("admin")
	path := "/e/local/p/web/a/coder-1/terminal"

	writer, err := root.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.CloseNow()
	writer.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	readUntil(t, writer, "attached to web/coder-1 (100x30)")
	writer.Write(ctx, websocket.MessageBinary, []byte("hello\r"))
	readUntil(t, writer, "you said: hello")
	writer.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	readUntil(t, writer, "resized to 120x40")

	// the agent says who is at the terminal
	if _, v := root.get("/e/local/p/web/a/coder-1"); at(v, "agent", "holder") != "admin" {
		t.Errorf("presence missing: %v", v)
	}

	// a second person is told it is in use, and may take it over
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.Administrator)
	busy, err := alice.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.CloseNow()
	if m := readControl(t, busy); m["type"] != "busy" || m["holder"] != "admin" {
		t.Errorf("the second terminal should be refused: %v", m)
	}
	taker, err := alice.dial(path + "?takeover=1")
	if err != nil {
		t.Fatal(err)
	}
	defer taker.CloseNow()
	taker.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`))
	readUntil(t, taker, "attached to web/coder-1 (80x24)")
	if m := readControl(t, writer); m["type"] != "taken" || m["holder"] != "alice" {
		t.Errorf("the first terminal should be told: %v", m)
	}
	taker.Write(ctx, websocket.MessageBinary, []byte("mine\r"))
	readUntil(t, taker, "you said: mine")
}

func TestTheTerminalRefusesWhatTheAPIRefuses(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	r.browser().signIn("admin")
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
	root.signIn("admin")
	u, _ := url.Parse(root.base)
	var cookies []string
	for _, c := range root.client.Jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	_, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(root.base, "http")+"/api/v1/e/local/p/web/a/coder-1/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {strings.Join(cookies, "; ")}, "Origin": {"https://evil.example"}},
	})
	if err == nil {
		t.Error("a cross-origin websocket was accepted")
	}
}

func TestSpecAndLogsAreReadOnlyText(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	b := r.browser()
	b.signIn("admin")
	if _, v := b.get("/e/local/p/web/spec"); !strings.Contains(v["text"].(string), "name: web") {
		t.Errorf("spec: %v", v)
	}
	if _, v := b.get("/e/local/p/web/a/coder-1/logs"); !strings.Contains(v["text"].(string), "log of coder-1") {
		t.Errorf("logs: %v", v)
	}
}

func TestAuditListsRefusals(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.hub.Scan(ctx)
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, "local", "web", r.userID("alice"), rbac.User)
	alice.post("/e/local/p/web/a/coder-1/rm", nil)
	_, v := root.get("/e/local/p/web/audit")
	var refused map[string]any
	for _, e := range list(v, "entries") {
		if e["allowed"] == false {
			refused = e
		}
	}
	if refused == nil || refused["action"] != "agent.rm" || refused["actor"] != "alice" || refused["agent"] != "coder-1" {
		t.Errorf("audit = %v", v)
	}
}
