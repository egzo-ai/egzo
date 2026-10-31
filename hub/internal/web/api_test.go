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

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestTheAppIsServedForItsOwnAddressesAndTheAPIAnswersJSONWhenSignedOut(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	for _, path := range []string{"/", "/login", "/settings/links", "/link/QXRB-7KPM", "/p/p0123456789ab", "/p/p0123456789ab/a/coder-1/logs"} {
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
	for _, path := range []string{"/session", "/projects", "/p/p0123456789ab", "/p/p0123456789ab/a/x/terminal", "/links", "/register/QXRB-7KPM"} {
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
	r := newRig(t)
	r.link("web", "coder")
	b := r.browser()
	b.signIn("admin")
	code, session := b.get("/session")
	if code != 200 || at(session, "user", "username") != "admin" || at(session, "user", "isOperator") != true {
		t.Fatalf("session = %d %v", code, session)
	}
	_, projects := b.get("/projects")
	got := list(projects, "projects")
	if len(got) != 1 || got[0]["name"] != "web" || got[0]["id"] != r.id("web") || got[0]["host"] != "laptop" || got[0]["role"] != "owner" || got[0]["operator"] != true {
		t.Errorf("projects = %v", projects)
	}
	if grants, _ := r.store.GrantsOf(ctx, r.userID("admin")); len(grants) != 1 {
		t.Errorf("an operator holds a project without a grant, but for what they approved: %+v", grants)
	}
}

func TestANewcomerSeesNothing(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.browser().signIn("admin")
	b := r.browser()
	b.signIn("alice")
	_, projects := b.get("/projects")
	if got := list(projects, "projects"); len(got) != 0 {
		t.Errorf("a newcomer sees %v", got)
	}
	if code, _ := b.get("/p/" + r.id("web")); code != 404 {
		t.Errorf("a project without a role = %d, want 404", code)
	}
	if code, _ := b.get("/links"); code != 403 {
		t.Errorf("the links for a non-operator = %d", code)
	}
}

func TestAProjectOffersOnlyWhatTheRoleAllows(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, r.id("web"), r.userID("alice"), rbac.User)

	_, owner := root.get("/p/" + r.id("web"))
	if owner["canSpawn"] != true || owner["canManage"] != true || owner["canAudit"] != true || owner["role"] != "owner" {
		t.Errorf("owner view = %v", owner)
	}
	a := list(owner, "agents")[0]
	if a["canOperate"] != true || a["status"] != "reading the code" || a["creator"] != "operator (CLI)" {
		t.Errorf("owner's agent = %v", a)
	}
	_, user := alice.get("/p/" + r.id("web"))
	if user["canSpawn"] != true || user["canManage"] != false || user["canAudit"] != false {
		t.Errorf("user view = %v", user)
	}
	if list(user, "agents")[0]["canOperate"] != false {
		t.Errorf("a user may operate the operator's agent: %v", user)
	}
	if code, _ := alice.post("/p/"+r.id("web")+"/a/coder-1/stop", nil); code != 403 {
		t.Errorf("a user stopping an agent of the operator = %d", code)
	}
	for _, path := range []string{"/members", "/audit"} {
		if code, _ := alice.get("/p/" + r.id("web") + path); code != 403 {
			t.Errorf("%s for a user = %d", path, code)
		}
	}
}

func TestTheAgentsActivityComesFromTheStatusFeedAndTheFeedIsReported(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	working := agent("coder-1", "operator")
	working.Activity, working.Waiting = "working", true
	r.fakes["web"].SetAgent(working)
	silent := fake.New("mute", "laptop", "coder")
	silent.SetAgent(agent("coder-1", "operator"))
	silent.SetFeedSilent(true) // its control sidecar logs no status
	r.adopt(silent)
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
	_, page := b.get("/p/" + r.id("mute"))
	if at(page, "feed", "state") != "unavailable" || list(page, "agents")[0]["activity"] != "unknown" {
		t.Errorf("project page = %v", page)
	}
}

func TestSpawnStopStartAndRemoveAreTasks(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	b := r.browser()
	b.signIn("admin")
	uid := r.userID("admin")
	agentNamed := func(name string) map[string]any {
		_, v := b.get("/p/" + r.id("web"))
		for _, a := range list(v, "agents") {
			if a["name"] == name {
				return a
			}
		}
		return nil
	}
	taskOf := func(a map[string]any) map[string]any {
		task, _ := a["task"].(map[string]any)
		return task
	}

	// a spawn needs a name, and is answered at once, before the agent exists, which is in the project from then on
	if code, out := b.post("/p/"+r.id("web")+"/spawn", json_{"template": "coder"}); code != http.StatusBadRequest {
		t.Errorf("a spawn without a name = %d %v", code, out)
	}
	r.fakes["web"].Hold()
	code, out := b.post("/p/"+r.id("web")+"/spawn", json_{"template": "coder", "name": "fix-bug"})
	started, _ := out["task"].(map[string]any)
	if code != http.StatusAccepted || started["state"] != "running" || started["id"] == "" || started["verb"] != "spawn" || started["agent"] != "fix-bug" ||
		started["template"] != "coder" || started["creator"] != "admin" {
		t.Fatalf("spawn = %d %v", code, out)
	}
	a := agentNamed("fix-bug")
	if a == nil || a["state"] != "" || taskOf(a)["id"] != started["id"] || taskOf(a)["step"] != "create network web-fix-bug" {
		t.Errorf("while it is made: %v", a)
	}
	if code, out := b.post("/p/"+r.id("web")+"/spawn", json_{"template": "coder", "name": "fix-bug"}); code != http.StatusConflict ||
		out["message"] != "fix-bug is being made by admin. Wait for it to end." {
		t.Errorf("a second spawn of the name = %d %v", code, out)
	}
	if code, _ := b.del("/p/" + r.id("web") + "/tasks/" + started["id"].(string)); code != http.StatusConflict {
		t.Errorf("dismissing a task that runs = %d", code)
	}
	r.fakes["web"].Release()
	r.hub.WaitForTasks()
	if a := agentNamed("fix-bug"); a["state"] != "running" || a["task"] != nil {
		t.Errorf("once made: %v", a)
	}
	if code, out := b.get("/p/" + r.id("web") + "/tasks/" + started["id"].(string)); code != 200 || taskOf(out)["state"] != "done" || taskOf(out)["endedAt"] == nil {
		t.Errorf("the task once ended = %d %v", code, out)
	}
	if calls := r.fakes["web"].Calls(); calls[0] != "spawn fix-bug user:"+uid {
		t.Errorf("calls = %v", calls)
	}
	if code, out := b.post("/p/"+r.id("web")+"/spawn", json_{"template": "coder", "name": "fix-bug"}); code != http.StatusConflict ||
		out["message"] != "There is already an agent named fix-bug. Pick another name." {
		t.Errorf("a spawn of a name an agent has = %d %v", code, out)
	}

	// start, stop and rm too
	for _, step := range []struct {
		verb    string
		running bool
	}{{"stop", false}, {"start", true}} {
		code, out := b.post("/p/"+r.id("web")+"/a/fix-bug/"+step.verb, nil)
		if task, _ := out["task"].(map[string]any); code != http.StatusAccepted || task["verb"] != step.verb {
			t.Errorf("%s = %d %v", step.verb, code, out)
		}
		r.hub.WaitForTasks()
		if a := agentNamed("fix-bug"); a["running"] != step.running {
			t.Errorf("after %s: %v", step.verb, a)
		}
	}
	if code, _ := b.post("/p/"+r.id("web")+"/a/fix-bug/rm", nil); code != http.StatusAccepted {
		t.Errorf("rm = %d", code)
	}
	r.hub.WaitForTasks()
	if code, _ := b.get("/p/" + r.id("web") + "/a/fix-bug"); code != 404 {
		t.Errorf("a removed agent = %d", code)
	}

	// a failed spawn stays an agent, with why, until it is dismissed; the activity keeps it
	_, out = b.post("/p/"+r.id("web")+"/spawn", json_{"template": "ghost", "name": "lost"})
	failed, _ := out["task"].(map[string]any)
	r.hub.WaitForTasks()
	if task := taskOf(agentNamed("lost")); task["state"] != "failed" || !strings.Contains(task["error"].(string), "ghost") || task["canDismiss"] != true {
		t.Errorf("a spawn that fails = %v", task)
	}
	if code, out := b.del("/p/" + r.id("web") + "/tasks/" + failed["id"].(string)); code != 200 || out["ok"] != true {
		t.Errorf("dismiss = %d %v", code, out)
	}
	if a := agentNamed("lost"); a != nil {
		t.Errorf("a dismissed spawn is an agent: %v", a)
	}
	if code, _ := b.del("/p/" + r.id("web") + "/tasks/" + failed["id"].(string)); code != 404 {
		t.Errorf("dismissing it again = %d", code)
	}
	_, out = b.get("/p/" + r.id("web") + "/tasks")
	if rows := list(out, "tasks"); len(rows) != 5 || rows[0]["agent"] != "lost" || rows[0]["state"] != "failed" || rows[4]["verb"] != "spawn" {
		t.Errorf("the activity = %v", rows)
	}
	if _, out = b.get("/p/" + r.id("web") + "/tasks?agent=fix-bug"); len(list(out, "tasks")) != 4 {
		t.Errorf("one agent's activity = %v", out)
	}
	if code, _ := b.post("/p/"+r.id("web")+"/a/x/explode", nil); code != 404 {
		t.Errorf("an unknown verb = %d", code)
	}
}

func TestAnAgentWaitingOnAQuestionAndTheTimelineWithoutMessageTexts(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	a := agent("coder-1", "operator")
	a.Waiting = true
	r.fakes["web"].SetAgent(a)
	r.fakes["web"].AddEvent(links.Event{Seq: 1, Time: time.Now(), Type: "status", Agent: "coder-1", Actor: "agent:coder-1", Text: "parsing the grammar"})
	b := r.browser()
	b.signIn("admin")
	_, v := b.get("/p/" + r.id("web") + "/a/coder-1")
	got := v["agent"].(map[string]any)
	if got["waiting"] != true || got["status"] != "reading the code" || got["activity"] != "idle" {
		t.Errorf("agent = %v", got)
	}
	_, events := b.get("/p/" + r.id("web") + "/events?agent=coder-1")
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
	r := newRig(t)
	r.link("web", "coder")
	b := r.browser()
	b.signIn("admin")
	try := func(origin, contentType string) int {
		req, _ := http.NewRequest("POST", b.base+"/api/v1/p/"+r.id("web")+"/spawn", strings.NewReader(`{"template":"coder","name":"x"}`))
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
	if calls := r.fakes["web"].Calls(); len(calls) != 0 {
		t.Errorf("a refused request did something: %v", calls)
	}
}

func TestMembersCanBeAddedChangedAndRemovedByAnOwner(t *testing.T) {
	r := newRig(t)
	r.link("web")
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	uid := r.userID("alice")

	_, m := root.get("/p/" + r.id("web") + "/members")
	if cands := list(m, "candidates"); len(cands) != 1 || cands[0]["email"] != "alice@example.com" || m["me"] != r.userID("admin") {
		t.Fatalf("members = %v", m)
	}
	// the operator is listed, as owner, marked: their access is not a role
	if rows := list(m, "members"); len(rows) != 1 || at(rows[0], "user", "username") != "admin" || rows[0]["role"] != "owner" || rows[0]["operator"] != true {
		t.Errorf("members = %v", rows)
	}
	if code, _ := root.post("/p/"+r.id("web")+"/members", json_{"user": uid, "role": "administrator"}); code != 200 {
		t.Errorf("grant = %d", code)
	}
	if role, _ := r.store.Role(ctx, r.id("web"), uid); role != rbac.Administrator {
		t.Errorf("role = %q", role)
	}
	if code, _ := alice.get("/p/" + r.id("web") + "/audit"); code != 200 {
		t.Errorf("a new administrator cannot read the audit log: %d", code)
	}
	if code, _ := alice.post("/p/"+r.id("web")+"/members", json_{"user": uid, "role": "owner"}); code != 403 {
		t.Errorf("an administrator granting owner = %d", code)
	}
	root.post("/p/"+r.id("web")+"/members", json_{"user": uid, "role": "owner"})
	if code, out := alice.post("/p/"+r.id("web")+"/members", json_{"user": uid, "role": "none"}); code != 403 || !strings.Contains(out["message"].(string), "cannot remove or lower themselves") {
		t.Errorf("an owner removing themselves = %d %v", code, out)
	}
	// an operator's access is not a role, whoever asks and whatever the role
	for _, who := range []*browser{root, alice} {
		for _, role := range []string{"none", "user", "owner"} {
			if code, out := who.post("/p/"+r.id("web")+"/members", json_{"user": r.userID("admin"), "role": role}); code != 403 || !strings.Contains(out["message"].(string), "operator holds every project") {
				t.Errorf("setting the operator to %s = %d %v", role, code, out)
			}
		}
	}
	root.post("/p/"+r.id("web")+"/members", json_{"user": uid, "role": "none"})
	if code, _ := alice.get("/p/" + r.id("web")); code != 404 {
		t.Errorf("a removed member still sees the project: %d", code)
	}
}

func TestOnlyAnOperatorListsUsersAndMakesOperators(t *testing.T) {
	r := newRig(t)
	r.link("web")
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
	if code, _ := alice.get("/links"); code != 200 {
		t.Errorf("a new operator manages links: %d", code)
	}
	if _, projects := alice.get("/projects"); len(list(projects, "projects")) != 1 {
		t.Errorf("a new operator holds every project: %v", projects)
	}
	if code, _ := root.put("/users/"+r.userID("admin"), json_{"isOperator": false}); code != 200 {
		t.Errorf("an operator stepping down while another is one = %d", code)
	}
	if code, _ := root.get("/links"); code != 403 {
		t.Errorf("a demoted operator still manages links: %d", code)
	}
	if code, out := alice.put("/users/"+uid, json_{"isOperator": false}); code != 409 || out["code"] != "conflict" {
		t.Errorf("the last operator stepping down = %d %v", code, out)
	}
	if code, _ := alice.put("/users/"+uid, json_{"isOperator": true}); code != 200 {
		t.Errorf("setting the same value = %d", code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	r := newRig(t)
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
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	root := r.browser()
	root.signIn("admin")
	path := "/p/" + r.id("web") + "/a/coder-1/terminal"

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
	if _, v := root.get("/p/" + r.id("web") + "/a/coder-1"); at(v, "agent", "holder") != "admin" {
		t.Errorf("presence missing: %v", v)
	}

	// a second person is told it is in use, and may take it over
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, r.id("web"), r.userID("alice"), rbac.Administrator)
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
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	r.browser().signIn("admin")
	eve := r.browser()
	eve.signIn("eve")
	if _, err := eve.dial("/p/" + r.id("web") + "/a/coder-1/terminal"); err == nil {
		t.Error("a person without a role opened a terminal")
	}
	anon := r.browser()
	if _, err := anon.dial("/p/" + r.id("web") + "/a/coder-1/terminal"); err == nil {
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
	_, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(root.base, "http")+"/api/v1/p/"+r.id("web")+"/a/coder-1/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {strings.Join(cookies, "; ")}, "Origin": {"https://evil.example"}},
	})
	if err == nil {
		t.Error("a cross-origin websocket was accepted")
	}
}

func TestSpecAndLogsAreReadOnlyText(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	b := r.browser()
	b.signIn("admin")
	if _, v := b.get("/p/" + r.id("web") + "/spec"); !strings.Contains(v["text"].(string), "name: web") {
		t.Errorf("spec: %v", v)
	}
	if _, v := b.get("/p/" + r.id("web") + "/a/coder-1/logs"); !strings.Contains(v["text"].(string), "log of coder-1") {
		t.Errorf("logs: %v", v)
	}
}

func TestAuditListsRefusals(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, r.id("web"), r.userID("alice"), rbac.User)
	alice.post("/p/"+r.id("web")+"/a/coder-1/rm", nil)
	_, v := root.get("/p/" + r.id("web") + "/audit")
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
