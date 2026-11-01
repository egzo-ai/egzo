package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// codeOf is the code in the link a person is handed.
func codeOf(t *testing.T, r *rig, made json_) string {
	t.Helper()
	link, _ := made["url"].(string)
	base, code, ok := strings.Cut(link, "/invite#")
	if !ok || base != r.server.URL || !strings.HasPrefix(code, store.InvitePrefix) {
		t.Fatalf("the invite's link is %q", link)
	}
	return code
}

func TestAStrangerAtTheProviderIsRefusedAndNoAccountIsMade(t *testing.T) {
	r := newRig(t)
	final := r.browser().sso(r.server.URL+"/login/start", "stranger")
	if final.Path != "/login" || final.Query().Get("error") != "no_invite" {
		t.Errorf("the sign-in ended on %s", final)
	}
	if _, err := r.store.UserByName(ctx, "stranger"); err == nil {
		t.Error("a user was made")
	}
}

func TestAnInviteLinkBringsANewPersonInThroughTheProvider(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	root := r.browser()
	root.signIn("admin")
	status, made := root.post("/p/"+r.id("web")+"/invites", json_{"role": "user"})
	if status != 200 {
		t.Fatalf("invite = %d %v", status, made)
	}
	code := codeOf(t, r, made)

	b := r.browser()
	status, seen := b.post("/invites/peek", json_{"code": code})
	if status != 200 || seen["sso"] != "Corp" || seen["local"] != true || at(seen, "invite", "projectName") != "web" || at(seen, "invite", "role") != "user" || at(seen, "invite", "createdBy") != "admin" {
		t.Errorf("peek = %d %v", status, seen)
	}
	if final := b.sso(b.ssoAddress(code), "alice"); final.Path != "/" {
		t.Fatalf("the sign-in ended on %s", final)
	}
	if status, project := b.get("/p/" + r.id("web")); status != 200 || project["role"] != "user" {
		t.Errorf("alice's project = %d %v", status, project)
	}
	if status, _ := r.browser().post("/invites/peek", json_{"code": code}); status != 404 {
		t.Errorf("peek once spent = %d", status)
	}
	if status, out := r.browser().post("/invites/sso", json_{"code": code}); status != 404 || out["code"] != "bad_invite" {
		t.Errorf("sign-in once spent = %d %v", status, out)
	}
}

func TestAnInviteOfAPersonTheHubKnowsIsAcceptedOnSignIn(t *testing.T) {
	r := newRig(t)
	r.link("web")
	r.browser().signIn("alice")
	code := r.invite(store.Invite{CreatedBy: r.userID("admin"), Project: r.id("web"), Role: rbac.Admin})
	b := r.browser()
	if final := b.sso(b.ssoAddress(code), "alice"); final.Path != "/" || final.Query().Get("invite") != "" {
		t.Fatalf("the sign-in ended on %s", final)
	}
	if status, project := b.get("/p/" + r.id("web")); status != 200 || project["role"] != "admin" {
		t.Errorf("alice's project = %d %v", status, project)
	}
}

func TestAnInviteAcceptedWhileSignedIn(t *testing.T) {
	r := newRig(t)
	r.link("web")
	b := r.browser()
	b.signIn("alice")
	code := r.invite(store.Invite{CreatedBy: r.userID("admin"), Project: r.id("web"), Role: rbac.User})
	if status, out := b.post("/invites/accept", json_{"code": code}); status != 200 || at(out, "invite", "project") != r.id("web") {
		t.Errorf("accept = %d %v", status, out)
	}
	if status, out := b.post("/invites/accept", json_{"code": code}); status != 404 || out["code"] != "bad_invite" {
		t.Errorf("accepted twice = %d %v", status, out)
	}
	if status, _ := r.browser().post("/invites/accept", json_{"code": "x"}); status != 401 {
		t.Errorf("accept with no session = %d", status)
	}
}

func TestALocalAccountFromAnInviteSignsItsOwnerIn(t *testing.T) {
	r := newRig(t)
	r.link("web")
	code := r.invite(store.Invite{CreatedBy: r.userID("admin"), Project: r.id("web"), Role: rbac.Viewer})
	b := r.browser()
	const password = "correct horse battery"
	if status, out := b.post("/invites/register", json_{"code": "nonsense", "username": "sam", "password": password}); status != 404 || out["code"] != "bad_invite" {
		t.Errorf("a bad code = %d %v", status, out)
	}
	if status, out := b.post("/invites/register", json_{"code": code, "username": "admin", "password": password}); status != 400 {
		t.Errorf("a reserved name = %d %v", status, out)
	}
	if status, _ := b.post("/invites/register", json_{"code": code, "username": "sam", "password": "short"}); status != 400 {
		t.Errorf("a short password = %d", status)
	}
	if status, out := b.post("/invites/register", json_{"code": code, "username": "sam", "password": password}); status != 200 {
		t.Fatalf("register = %d %v", status, out)
	}
	if status, session := b.get("/session"); status != 200 || at(session, "user", "username") != "sam" {
		t.Errorf("session = %d %v", status, session)
	}
	// a viewer is on the hub and is shown nothing of the project
	if _, projects := b.get("/projects"); len(list(projects, "projects")) != 0 {
		t.Errorf("a viewer lists %v", projects)
	}
	if status, _ := b.get("/p/" + r.id("web")); status != 404 {
		t.Errorf("a viewer opens the project: %d", status)
	}
	again := r.browser()
	if status, out := again.password("sam", password); status != 200 {
		t.Errorf("signing in = %d %v", status, out)
	}
}

func TestLocalAccountsNeedLocalLogin(t *testing.T) {
	r := newRig(t)
	code := r.invite(store.Invite{CreatedBy: r.userID("admin")})
	if _, err := r.hub.SetupSignIn(ctx, hub.SignInOptions{Issuer: r.signIn.Issuer}); err != nil {
		t.Fatal(err)
	}
	b := r.browser()
	if status, seen := b.post("/invites/peek", json_{"code": code}); status != 200 || seen["local"] != false {
		t.Errorf("peek = %d %v", status, seen)
	}
	if status, _ := b.post("/invites/register", json_{"code": code, "username": "sam", "password": "correct horse battery"}); status != 404 {
		t.Errorf("register = %d", status)
	}
}

func TestACodeNeverTravelsInAnAddress(t *testing.T) {
	r := newRig(t)
	code := r.invite(store.Invite{CreatedBy: r.userID("admin")})
	b := r.browser()
	target, _ := url.Parse(b.ssoAddress(code))
	if strings.Contains(target.String(), code) {
		t.Errorf("the provider's address holds the code: %s", target)
	}
	if status, _ := b.get("/invites/peek"); status == 200 {
		t.Error("peek answers a GET, which an address could carry")
	}
}

func TestInvitesOfTheHubAreForOperators(t *testing.T) {
	r := newRig(t)
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	for _, call := range []func() (int, json_){
		func() (int, json_) { return alice.get("/invites") },
		func() (int, json_) { return alice.post("/invites", json_{"operator": true}) },
	} {
		if status, _ := call(); status != 403 {
			t.Errorf("a person = %d", status)
		}
	}
	status, made := root.post("/invites", json_{"projectQuota": 2})
	if status != 200 {
		t.Fatalf("invite = %d %v", status, made)
	}
	code := codeOf(t, r, made)
	_, listing := root.get("/invites")
	rows := list(listing, "invites")
	if len(rows) != 1 || rows[0]["projectQuota"] != float64(2) || strings.Contains(asJSON(listing), code) {
		t.Errorf("invites = %v", listing)
	}
	if status, out := root.del("/invites/" + rows[0]["id"].(string)); status != 200 {
		t.Errorf("revoke = %d %v", status, out)
	}
	if status, _ := r.browser().post("/invites/peek", json_{"code": code}); status != 404 {
		t.Errorf("peek once revoked = %d", status)
	}
}

func TestAProjectsInvitesAreItsOwners(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	r.store.SetRole(ctx, id, r.userID("alice"), rbac.Admin)
	if status, _ := alice.post("/p/"+id+"/invites", json_{"role": "user"}); status != 403 {
		t.Errorf("an administrator inviting = %d", status)
	}
	if status, _ := root.post("/p/"+id+"/invites", json_{"role": "boss"}); status != 400 {
		t.Errorf("what is not a role = %d", status)
	}
	root.post("/p/"+id+"/invites", json_{"role": "viewer"})
	if _, out := root.get("/p/" + id + "/invites"); len(list(out, "invites")) != 1 {
		t.Errorf("invites = %v", out)
	}
	if status, _ := alice.get("/p/" + id + "/invites"); status != 403 {
		t.Errorf("an administrator listing = %d", status)
	}
}

func TestAnOwnerUnlinksAndAQuotaLetsAPersonConnect(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	if _, session := alice.get("/session"); session["canLink"] != false {
		t.Errorf("no quota, and canLink = %v", session["canLink"])
	}
	if status, out := alice.post("/p/"+id+"/unlink", json_{}); status != 404 {
		t.Errorf("a stranger unlinking = %d %v", status, out)
	}
	if status, out := root.put("/users/"+r.userID("alice"), json_{"projectQuota": 1}); status != 200 {
		t.Fatalf("set the quota = %d %v", status, out)
	}
	if status, out := root.put("/users/"+r.userID("alice"), json_{"projectQuota": -1}); status != 400 {
		t.Errorf("a negative quota = %d %v", status, out)
	}
	if status, out := root.put("/users/"+r.userID("alice"), json_{"projectQuota": 1, "disabled": true}); status != 400 {
		t.Errorf("two at once = %d %v", status, out)
	}
	if status, _ := alice.put("/users/"+r.userID("alice"), json_{"projectQuota": 9}); status != 403 {
		t.Errorf("a person raising their own quota = %d", status)
	}
	if _, session := alice.get("/session"); session["canLink"] != true || at(session, "user", "projectQuota") != float64(1) {
		t.Errorf("session = %v", session)
	}
	if status, out := alice.post("/links/codes", json_{}); status != 200 {
		t.Errorf("a code with quota = %d %v", status, out)
	}
	if _, codes := alice.get("/links/codes"); len(list(codes, "codes")) != 1 {
		t.Errorf("alice's codes = %v", codes)
	}
	if status, _ := alice.get("/links"); status != 403 {
		t.Errorf("a person listing the links = %d", status)
	}
	if status, out := root.post("/p/"+id+"/unlink", json_{}); status != 200 {
		t.Fatalf("unlink = %d %v", status, out)
	}
	if status, _ := root.get("/p/" + id); status != 404 {
		t.Errorf("the project stays: %d", status)
	}
}

func TestOperatorsSeeAProjectLeftWithoutAnOwner(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	root.post("/p/"+id+"/members", json_{"user": r.userID("alice"), "role": "owner"})
	root.post("/p/"+id+"/members", json_{"user": r.userID("admin"), "role": "none"})
	_, project := root.get("/p/" + id)
	if project["role"] != "overseer" || project["ownerless"] != nil || project["canMembers"] != true || project["canManage"] != false {
		t.Errorf("project = %v", project)
	}
	root.put("/users/"+r.userID("alice"), json_{"disabled": true})
	if _, project := root.get("/p/" + id); project["ownerless"] != true {
		t.Errorf("project = %v", project)
	}
	if _, projects := root.get("/projects"); list(projects, "projects")[0]["ownerless"] != true {
		t.Errorf("projects = %v", projects)
	}
	_, linksOut := root.get("/links")
	if row := list(linksOut, "links")[0]; len(row["owners"].([]any)) != 0 || row["owner"] != "admin" {
		t.Errorf("links = %v", linksOut)
	}
	if status, out := root.get("/p/" + id + "/members"); status != 200 || len(list(out, "members")) != 1 {
		t.Errorf("members = %d %v", status, out)
	}
	if status, out := root.post("/p/"+id+"/members", json_{"user": r.userID("admin"), "role": "owner"}); status != 200 {
		t.Errorf("an operator taking it = %d %v", status, out)
	}
}

func TestTheLastOwnerIsToldWhy(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	root := r.browser()
	root.signIn("admin")
	status, out := root.post("/p/"+id+"/members", json_{"user": r.userID("admin"), "role": "none"})
	if status != http.StatusConflict || !strings.Contains(out["message"].(string), "at least one owner") {
		t.Errorf("the last owner leaving = %d %v", status, out)
	}
}

func asJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
