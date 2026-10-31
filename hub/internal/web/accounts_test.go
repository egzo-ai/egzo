package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

func TestTheSignInPageIsToldWhichWaysInExist(t *testing.T) {
	r := newRig(t)
	code, out := r.browser().get("/signin")
	if code != 200 || out["local"] != true || out["sso"] != "Corp" || out["redirect"] != r.server.URL+"/auth/callback" {
		t.Errorf("signin = %d %v", code, out)
	}
	noSSO := newRigWith(t, []func(*rig){func(r *rig) { r.signIn.Issuer = "" }})
	if _, out := noSSO.browser().get("/signin"); out["local"] != true || out["sso"] != "" {
		t.Errorf("without single sign-on: %v", out)
	}
}

func TestThePasswordSignInAnswersTheSameToEveryFailure(t *testing.T) {
	r := newRig(t)
	r.browser().signIn("alice") // a user with no password
	b := r.browser()
	for _, c := range [][2]string{{"admin", "wrong"}, {"nobody", adminPassword}, {"alice", ""}} {
		code, out := b.password(c[0], c[1])
		if code != 401 || out["message"] != "Wrong username or password." {
			t.Errorf("%v = %d %v", c, code, out)
		}
	}
	if code, _ := b.get("/session"); code != 401 {
		t.Error("a failed sign-in made a session")
	}
	if code, _ := b.password("Admin", adminPassword); code != 200 {
		t.Errorf("the right password = %d", code)
	}
	if code, out := b.get("/session"); code != 200 || at(out, "user", "username") != "admin" || out["hasPassword"] != true {
		t.Errorf("session = %d %v", code, out)
	}
}

func TestThePasswordSignInNeedsTheOriginAndJSON(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	for _, c := range []struct{ origin, contentType string }{{"https://evil.example", "application/json"}, {"", "application/json"}, {b.base, "application/x-www-form-urlencoded"}} {
		req, _ := http.NewRequest("POST", b.base+"/auth/login", strings.NewReader(`{"username":"admin","password":"`+adminPassword+`"}`))
		req.Header.Set("Content-Type", c.contentType)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		resp, _ := b.client.Do(req)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%+v signed in", c)
		}
	}
}

func TestRepeatedFailuresSlowTheAccountDown(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	for range 3 {
		b.password("admin", "wrong")
	}
	code, out := b.password("admin", adminPassword)
	if code != http.StatusTooManyRequests || !strings.Contains(out["message"].(string), "Try again in") {
		t.Errorf("after 3 failures = %d %v", code, out)
	}
	if code, _ := r.browser().password("someone-else", "x"); code != 401 {
		t.Errorf("another account is not slowed: %d", code)
	}
}

func TestWithLocalLoginOffThereIsNoPasswordSignIn(t *testing.T) {
	r := newRig(t)
	admin := r.userID("admin")
	r.store.LinkIdentity(ctx, admin, r.provider.URL, "sub-boss", store.Profile{})
	r.signIn.LocalLogin = false // as a restart with EGZO_LOCAL_LOGIN=false
	if _, err := r.hub.SetupSignIn(ctx, r.signIn); err != nil {
		t.Fatal(err)
	}
	b := r.browser()
	if code, _ := b.password("admin", adminPassword); code != 404 {
		t.Errorf("password sign-in with local login off = %d", code)
	}
	if _, out := b.get("/signin"); out["local"] != false {
		t.Errorf("signin = %v", out)
	}
	b.signIn("boss")
	if _, out := b.get("/session"); at(out, "user", "username") != "admin" {
		t.Errorf("admin signs in through SSO: %v", out)
	}
}

func TestANewPersonGetsAUsernameAndTheProvidersDetailsForDisplay(t *testing.T) {
	r := newRig(t)
	r.oidc.Profile = func(user string) map[string]any {
		return map[string]any{"email": "alex@corp.com", "email_verified": true, "name": "Alex Smith", "preferred_username": "alex@corp.com",
			"picture": map[string]string{"alex": "https://img.example/alex.png", "eve": "http://169.254.169.254/latest"}[user]}
	}
	b := r.browser()
	b.signIn("alex")
	_, out := b.get("/session")
	u, _ := out["user"].(map[string]any)
	if u["username"] != "alex" || u["name"] != "Alex Smith" || u["email"] != "alex@corp.com" || u["emailVerified"] != true || u["picture"] != "https://img.example/alex.png" || u["isOperator"] != false {
		t.Errorf("user = %v", u)
	}
	if ids := list(out, "identities"); len(ids) != 1 || ids[0]["issuer"] != r.provider.URL || ids[0]["current"] != true {
		t.Errorf("identities = %v", out["identities"])
	}
	eve := r.browser()
	eve.signIn("eve")
	_, out = eve.get("/session")
	if at(out, "user", "username") != "alex-2" || at(out, "user", "picture") != "" {
		t.Errorf("a second alex, and an avatar that is not https: %v", out["user"])
	}
}

func TestTheAvatarIsFetchedByTheBrowser(t *testing.T) {
	r := newRig(t)
	resp, _ := http.Get(r.server.URL + "/")
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: https:;") {
		t.Errorf("csp = %q", csp)
	}
}

func TestADisabledUserIsRefusedLosesTheirSessionsAndTheirTerminals(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	op := r.browser()
	op.signIn("admin")
	bob := r.browser()
	bob.signIn("bob")
	r.store.SetRole(ctx, r.id("web"), r.userID("bob"), rbac.Admin)
	term, err := bob.dial("/p/" + r.id("web") + "/a/coder-1/terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer term.CloseNow()
	term.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`))
	readUntil(t, term, "attached to web/coder-1")

	if code, out := op.put("/users/"+r.userID("bob"), json_{"disabled": true}); code != 200 {
		t.Fatalf("disable = %d %v", code, out)
	}
	closed(t, term)
	if code, _ := bob.get("/session"); code != 401 {
		t.Errorf("the disabled user's session = %d", code)
	}
	if final := r.browser().sso(r.server.URL+"/login/start", "bob"); final.Path != "/login" || final.Query().Get("error") != "Sign-in refused." {
		t.Errorf("a disabled user's sign-in ended on %s", final)
	}
	_, users := op.get("/users")
	for _, u := range list(users, "users") {
		if u["username"] == "bob" && u["disabled"] != true {
			t.Errorf("bob = %v", u)
		}
	}
	_, members := op.get("/p/" + r.id("web") + "/members")
	for _, c := range list(members, "candidates") {
		if c["username"] == "bob" {
			t.Error("a disabled user is offered as a candidate")
		}
	}
	if code, out := op.put("/users/"+r.userID("admin"), json_{"disabled": true}); code != 409 || !strings.Contains(out["message"].(string), "operator cannot be disabled") {
		t.Errorf("disabling an operator = %d %v", code, out)
	}
	if code, _ := bob.put("/users/"+r.userID("bob"), json_{"disabled": false}); code != 401 {
		t.Errorf("a disabled user enabling themselves = %d", code)
	}
	if code, _ := op.put("/users/"+r.userID("bob"), json_{"disabled": false}); code != 200 {
		t.Errorf("enable = %d", code)
	}
	bob.signIn("bob")
	if _, p := bob.get("/projects"); len(list(p, "projects")) != 1 {
		t.Errorf("an enabled user's grants did not come back: %v", p)
	}
}

func TestSigningOutClosesTheTerminalsOfThatSession(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	b := r.browser()
	b.signIn("admin")
	term, err := b.dial("/p/" + r.id("web") + "/a/coder-1/terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer term.CloseNow()
	term.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`))
	readUntil(t, term, "attached to web/coder-1")
	b.post("/logout", nil)
	closed(t, term)
}

// closed waits for the hub to close a terminal.
func closed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(c); err != nil {
			if c.Err() != nil {
				t.Fatal("the terminal stayed open")
			}
			return
		}
	}
}

// link starts linking at the provider as the signed-in user of b, signs in there as user, and returns where the hub sent the
// browser back.
func (b *browser) link(user string) *url.URL {
	b.t.Helper()
	code, out := b.post("/me/link", nil)
	if code != 200 {
		b.t.Fatalf("link = %d %v", code, out)
	}
	return b.sso(out["url"].(string), user)
}

func TestTheStarterAccountLinksSSOAndThenSignsInWithIt(t *testing.T) {
	r := newRig(t)
	admin := r.browser()
	admin.signIn("admin")
	if final := admin.link("boss"); final.Path != "/settings/account" || final.Query().Get("linked") != "1" {
		t.Fatalf("link ended on %s", final)
	}
	_, out := admin.get("/session")
	ids := list(out, "identities")
	if len(ids) != 1 || ids[0]["email"] != "boss@example.com" || at(out, "user", "username") != "admin" {
		t.Fatalf("session = %v", out)
	}
	sso := r.browser()
	sso.signIn("boss")
	if _, out := sso.get("/session"); at(out, "user", "username") != "admin" || at(out, "user", "isOperator") != true {
		t.Errorf("signing in with the linked account: %v", out)
	}
	users, _ := r.store.Users(ctx)
	if len(users) != 1 {
		t.Errorf("linking made a user: %d users", len(users))
	}

	// a second account at the same provider is refused; so is an account of somebody else
	if final := admin.link("boss2"); !strings.Contains(final.Query().Get("error"), "Unlink it first") {
		t.Errorf("a second account at one provider: %s", final)
	}
	r.browser().signIn("alice")
	admin.del("/me/identities/" + ids[0]["id"].(string))
	if final := admin.link("alice"); !strings.Contains(final.Query().Get("error"), "already belongs to alice") {
		t.Errorf("somebody else's account: %s", final)
	}
}

func TestALinkCannotBeFinishedByAnotherSignedInUser(t *testing.T) {
	r := newRig(t)
	admin := r.browser()
	admin.signIn("admin")
	_, out := admin.post("/me/link", nil)
	// the link cookie travels with admin's browser only; a browser signed in as somebody else that somehow got it is refused
	mallory := r.browser()
	mallory.signIn("mallory")
	u, _ := url.Parse(r.server.URL)
	for _, c := range admin.client.Jar.Cookies(u) {
		if c.Name == "egzo_login" {
			mallory.client.Jar.SetCookies(u, []*http.Cookie{c})
		}
	}
	if final := mallory.sso(out["url"].(string), "mallory-idp"); final.Path != "/login" {
		t.Errorf("mallory finished admin's link: %s", final)
	}
	if ids, _ := r.store.Identities(ctx, r.userID("admin")); len(ids) != 0 {
		t.Errorf("admin got an identity: %+v", ids)
	}
}

func TestUnlinkingKeepsAWayInAndEndsItsSessions(t *testing.T) {
	r := newRig(t)
	alice := r.browser()
	alice.signIn("alice")
	_, out := alice.get("/session")
	id := list(out, "identities")[0]["id"].(string)
	if code, out := alice.del("/me/identities/" + id); code != 409 || !strings.Contains(out["message"].(string), "last way to sign in") {
		t.Errorf("unlinking the last way in = %d %v", code, out)
	}
	admin := r.browser()
	admin.signIn("admin")
	admin.link("boss")
	other := r.browser()
	other.signIn("boss")
	_, out = admin.get("/session")
	id = list(out, "identities")[0]["id"].(string)
	if code, out := admin.del("/me/identities/" + id); code != 200 {
		t.Fatalf("unlink = %d %v", code, out)
	}
	if code, _ := other.get("/session"); code != 401 {
		t.Error("the session made with the unlinked account survived")
	}
	if code, _ := admin.get("/session"); code != 200 {
		t.Error("the password session ended")
	}
}

func TestAnOperatorFreesAnAccidentalUsersAccount(t *testing.T) {
	r := newRig(t)
	admin := r.browser()
	admin.signIn("admin")
	r.browser().signIn("boss") // signed in with SSO before linking: a second user
	accident := r.userID("boss")
	identities, _ := r.store.Identities(ctx, accident)
	path := "/users/" + accident + "/identities/" + identities[0].ID
	if code, _ := admin.del(path); code != 409 {
		t.Errorf("removing an enabled user's account = %d", code)
	}
	admin.put("/users/"+accident, json_{"disabled": true})
	if code, out := admin.del(path); code != 200 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if final := admin.link("boss"); final.Query().Get("linked") != "1" {
		t.Errorf("link after the removal ended on %s", final)
	}
}
