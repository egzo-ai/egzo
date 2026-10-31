package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/link"
)

// A real link: core's, the one `egzo project up` starts, against the hub's real endpoint. Its engine is an empty Docker API, enough for
// it to say who it is: the specs and `make hub-stack` have it run a real project.

const secretKey = "0123456789abcdef0123456789abcdef"

func emptyEngine(t *testing.T) *link.Engine {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/info"):
			fmt.Fprint(w, `{"Name":"the-laptop"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			fmt.Fprint(w, `{"Volumes":[]}`)
		default:
			fmt.Fprint(w, "[]")
		}
	}))
	t.Cleanup(server.Close)
	client, err := engine.Dial(context.Background(), engine.Target{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	return link.NewEngine(client)
}

// dial starts a link to hub, and stops it with the test. stop ends it as a machine that went away does.
func dial(t *testing.T, address string) (l *link.Link, stop func()) {
	t.Helper()
	l = link.New(link.Options{Project: "web", Hub: address, Dir: t.TempDir(), Engine: emptyEngine(t)})
	if err := l.Unlock([]byte(secretKey), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { defer close(done); l.Run(ctx) }()
	return l, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the link did not stop")
		}
	}
}

func (r *rig) state(l *link.Link, want string) hublink.Status {
	r.t.Helper()
	r.waitFor("the link to be "+want, func() bool { return l.Status().State == want })
	return l.Status()
}

func TestARealLinkRegistersIsApprovedAndIsForgottenWhenItUnlinks(t *testing.T) {
	r := newRig(t)
	l, _ := dial(t, r.linkURL)
	status := r.state(l, hublink.StatePending)
	if !strings.HasPrefix(status.Address, r.server.URL+"/link/") || status.Code == "" {
		t.Fatalf("pending = %+v", status)
	}
	regs, _ := r.store.Registrations(ctx)
	if len(regs) != 1 || regs[0].Name != "web" || regs[0].Host != "the-laptop" || regs[0].Code != status.Code {
		t.Fatalf("registrations = %+v", regs)
	}

	root := r.browser()
	root.signIn("admin")
	code, page := root.get("/register/" + status.Code)
	if code != 200 || at(page, "link", "name") != "web" || at(page, "link", "host") != "the-laptop" {
		t.Fatalf("the page of the code = %d %v", code, page)
	}
	alice := r.browser()
	alice.signIn("alice")
	if code, _ := alice.get("/register/" + status.Code); code != 403 {
		t.Errorf("the page of the code for a user = %d", code)
	}
	if code, _ := alice.post("/register/"+status.Code, json_{"approve": true}); code != 403 {
		t.Errorf("a user approving = %d", code)
	}
	if code, _ := root.post("/register/"+status.Code, json_{}); code != 400 {
		t.Errorf("a decision that says nothing = %d", code)
	}
	code, out := root.post("/register/"+status.Code, json_{"approve": true})
	if code != 200 || out["project"] != regs[0].ID {
		t.Fatalf("approve = %d %v", code, out)
	}
	status = r.state(l, hublink.StateApproved)
	if status.Project != regs[0].ID || status.Owner != "admin" {
		t.Errorf("approved = %+v", status)
	}
	if _, listed := root.get("/projects"); len(list(listed, "projects")) != 1 {
		t.Errorf("projects = %v", listed)
	}
	// a verb goes down the connection, and the link's own answer comes back: its engine has no such project
	code, out = root.get("/p/" + regs[0].ID + "/spec")
	if code != http.StatusBadGateway || !strings.Contains(out["message"].(string), "project") {
		t.Errorf("spec = %d %v", code, out)
	}

	if err := l.Unlink(ctx); err != nil {
		t.Fatal(err)
	}
	if code, _ := root.get("/p/" + regs[0].ID); code != 404 {
		t.Errorf("a forgotten project = %d", code)
	}
	if regs, _ = r.store.Registrations(ctx); len(regs) != 0 {
		t.Errorf("registrations = %+v", regs)
	}
}

func TestARealLinkThatGoesAwayIsOfflineSinceAndComesBackWithItsKey(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	start := func() (*link.Link, context.CancelFunc) {
		l := link.New(link.Options{Project: "web", Hub: r.linkURL, Dir: dir, Engine: emptyEngine(t)})
		if err := l.Unlock([]byte(secretKey), ""); err != nil {
			t.Fatal(err)
		}
		run, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go l.Run(run)
		return l, cancel
	}
	l, stop := start()
	status := r.state(l, hublink.StatePending)
	admin, _ := r.store.UserByName(ctx, "admin")
	reg, err := r.hub.Approve(ctx, admin, status.Code)
	if err != nil {
		t.Fatal(err)
	}
	r.state(l, hublink.StateApproved)

	stop()
	r.waitFor("the project to be offline", func() bool { got, _ := r.store.Registration(ctx, reg.ID); return !got.Online() })
	b := r.browser()
	b.signIn("admin")
	_, page := b.get("/p/" + reg.ID)
	if page["offlineSince"] == nil || at(page, "feed", "state") != "offline" {
		t.Errorf("offline = %v", page)
	}
	if code, out := b.post("/p/"+reg.ID+"/spawn", json_{"template": "coder", "name": "x"}); code != 409 || out["code"] != "offline" {
		t.Errorf("a spawn offline = %d %v", code, out)
	}

	l, _ = start()
	status = r.state(l, hublink.StateApproved)
	if status.Project != reg.ID {
		t.Errorf("it came back as %+v", status)
	}
	if got, _ := r.store.Registration(ctx, reg.ID); !got.Online() {
		t.Error("it came back offline")
	}
}

func TestALinkThatSignsForAnotherHubIsRefused(t *testing.T) {
	r := newRig(t)
	// a link that trusts some other hub, whose challenge came from this one: what it signed names the other
	l, _ := dial(t, r.linkURL+"/")
	r.state(l, hublink.StatePending) // the address with a slash is the same address

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		q.Host = strings.TrimPrefix(r.server.URL, "http://")
		r.server.Config.Handler.ServeHTTP(w, q)
	}))
	t.Cleanup(other.Close)
	liar, _ := dial(t, other.URL)
	time.Sleep(100 * time.Millisecond)
	if state := liar.Status().State; state == hublink.StatePending || state == hublink.StateApproved {
		t.Errorf("a proof for %s was taken: %+v", other.URL, liar.Status())
	}
	if regs, _ := r.store.Registrations(ctx); len(regs) != 1 {
		t.Errorf("registrations = %+v", regs)
	}
}

func TestAProofIsForTheConfiguredAddressWhateverTheRequestSays(t *testing.T) {
	r := newRig(t)
	// the Host header is the client's to choose: a hub that passes a challenge on says its own name there, and it must not help
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { r.server.Config.Handler.ServeHTTP(w, q) }))
	t.Cleanup(other.Close)
	l, _ := dial(t, other.URL)
	time.Sleep(100 * time.Millisecond)
	if state := l.Status().State; state == hublink.StatePending {
		t.Errorf("a proof for the Host of the request was taken: %+v", l.Status())
	}
}

func TestTheLinkEndpointAloneServesLinksAndNoPage(t *testing.T) {
	r := newRig(t)
	server := httptest.NewServer(r.app.LinkHandler())
	t.Cleanup(server.Close)
	for _, path := range []string{"/", "/login", "/api/v1/session", "/api/v1/signin", "/assets/app-1234.js"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s on the link listener = %d", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(server.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz = %+v, %v", resp, err)
	}
	resp, err = http.Get(server.URL + hublink.Path)
	if err != nil || resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("the endpoint without a WebSocket = %+v, %v", resp, err)
	}
}

func TestAProjectThatIsOfflineSaysSoInTheLists(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	b := r.browser()
	b.signIn("admin")
	_, projects := b.get("/projects")
	if got := list(projects, "projects"); len(got) != 1 || got[0]["offlineSince"] != nil {
		t.Fatalf("projects = %v", got)
	}
	f.Drop()
	r.waitFor("it to be offline", func() bool {
		_, projects = b.get("/projects")
		got := list(projects, "projects")
		return len(got) == 1 && got[0]["offlineSince"] != nil
	})
	if got := list(projects, "projects"); at(json_(got[0]), "feed", "state") != "offline" {
		t.Errorf("projects = %v", got)
	}
}

func TestAnOperatorListsRevokesAndForgetsLinks(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	waiting := fake.New("data", "server", "coder")
	r.connect(waiting)
	root := r.browser()
	root.signIn("admin")
	_, listed := root.get("/links")
	all := list(listed, "links")
	if len(all) != 2 {
		t.Fatalf("links = %v", all)
	}
	states := map[string]map[string]any{}
	for _, one := range all {
		states[one["state"].(string)] = one
	}
	if approved := states["approved"]; approved["owner"] != "admin" || approved["name"] != "web" {
		t.Errorf("approved = %v", approved)
	}
	if pending := states["pending"]; pending["code"] == "" || pending["host"] != "server" {
		t.Errorf("pending = %v", pending)
	}
	id := r.id("web")
	if code, out := root.del("/links/" + id); code != http.StatusConflict {
		t.Errorf("forgetting a linked project = %d %v", code, out)
	}
	if code, out := root.post("/links/"+id+"/revoke", json_{"message": "gone"}); code != 200 {
		t.Fatalf("revoke = %d %v", code, out)
	}
	if code, _ := root.get("/p/" + id); code != 404 {
		t.Errorf("a revoked project = %d", code)
	}
	if code, out := root.del("/links/" + id); code != 200 {
		t.Errorf("forget = %d %v", code, out)
	}
	if code, _ := root.del("/links/" + id); code != 404 {
		t.Errorf("forget twice = %d", code)
	}
}
