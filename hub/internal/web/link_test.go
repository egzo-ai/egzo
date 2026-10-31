package web_test

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/egzo-ai/egzo/hub/internal/store"
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

// register has a running link present a code, as `egzo project register` does.
func register(t *testing.T, l *link.Link, code string) hublink.Status {
	t.Helper()
	status, err := l.Register(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestARealLinkRegistersWithACodeAndIsForgottenWhenItUnlinks(t *testing.T) {
	r := newRig(t)
	l, _ := dial(t, r.linkURL)
	status := r.state(l, hublink.StateUnregistered)
	if !strings.Contains(status.Text(), "egzo project register") {
		t.Errorf("unregistered = %q", status.Text())
	}
	if regs, _ := r.store.Registrations(ctx); len(regs) != 0 {
		t.Fatalf("a link without a code left %+v", regs)
	}

	root := r.browser()
	root.signIn("admin")
	alice := r.browser()
	alice.signIn("alice")
	if code, out := alice.post("/links/codes", json_{}); code != 409 || out["code"] != "over_quota" {
		t.Errorf("a user with no quota making a code = %d %v", code, out)
	}
	code, made := root.post("/links/codes", json_{})
	if code != 200 || made["hub"] != r.linkURL || !strings.HasPrefix(made["code"].(string), "egzo-link-") {
		t.Fatalf("make a code = %d %v", code, made)
	}
	_, listed := root.get("/links/codes")
	codes := list(listed, "codes")
	if len(codes) != 1 || codes[0]["id"] != made["id"] || codes[0]["createdBy"] != "admin" || codes[0]["code"] != nil {
		t.Fatalf("codes = %v", codes)
	}

	status = register(t, l, made["code"].(string))
	regs, _ := r.store.Registrations(ctx)
	if status.State != hublink.StateApproved || status.Owner != "admin" || len(regs) != 1 || status.Project != regs[0].ID || regs[0].Host != "the-laptop" {
		t.Fatalf("registered = %+v, %+v", status, regs)
	}
	if strings.Contains(fmt.Sprintf("%+v", l.Status()), made["code"].(string)) {
		t.Error("the status shows the code")
	}
	if _, listed = root.get("/links/codes"); len(list(listed, "codes")) != 0 {
		t.Errorf("a code that was used is listed: %v", listed)
	}
	if _, listed := root.get("/projects"); len(list(listed, "projects")) != 1 {
		t.Errorf("projects = %v", listed)
	}
	// a verb goes down the connection, and the link's own answer comes back: its engine has no such project
	code, out := root.get("/p/" + regs[0].ID + "/spec")
	if code != http.StatusBadGateway || !strings.Contains(out["message"].(string), "project") {
		t.Errorf("spec = %d %v", code, out)
	}

	if state, err := l.Unlink(ctx); err != nil || state != hublink.StateUnlinked {
		t.Fatalf("unlink = %q, %v", state, err)
	}
	if code, _ := root.get("/p/" + regs[0].ID); code != 404 {
		t.Errorf("a forgotten project = %d", code)
	}
	if regs, _ = r.store.Registrations(ctx); len(regs) != 0 {
		t.Errorf("registrations = %+v", regs)
	}
}

func TestACodeCanBeCancelled(t *testing.T) {
	r := newRig(t)
	root := r.browser()
	root.signIn("admin")
	_, made := root.post("/links/codes", json_{})
	id := made["id"].(string)
	alice := r.browser()
	alice.signIn("alice")
	if code, _ := alice.del("/links/codes/" + id); code != 404 {
		t.Errorf("a user cancelling somebody else's = %d", code)
	}
	if code, out := root.del("/links/codes/" + id); code != 200 {
		t.Errorf("cancel = %d %v", code, out)
	}
	if code, _ := root.del("/links/codes/" + id); code != 404 {
		t.Errorf("cancelled twice = %d", code)
	}
	l, _ := dial(t, r.linkURL)
	r.state(l, hublink.StateUnregistered)
	if status := register(t, l, made["code"].(string)); status.State != hublink.StateUnregistered {
		t.Errorf("a cancelled code = %+v", status)
	}
}

func TestALinkThatIsTurnedAwayStopsDialingUntilSomethingChanges(t *testing.T) {
	r := newRig(t)
	l, _ := dial(t, r.linkURL)
	r.state(l, hublink.StateUnregistered)
	time.Sleep(1500 * time.Millisecond) // more than the first retry
	if got := r.dials.Load(); got != 1 {
		t.Fatalf("a link the hub said no to dialed %d times", got)
	}
	// `up` again: one more try, and the same answer
	if err := l.Unlock([]byte(secretKey), ""); err != nil {
		t.Fatal(err)
	}
	r.waitFor("it to dial again", func() bool { return r.dials.Load() == 2 })
	r.state(l, hublink.StateUnregistered)

	status := register(t, l, r.code())
	if status.State != hublink.StateApproved {
		t.Fatalf("registered = %+v", status)
	}
	// revoked: it says so, and stops dialing too
	admin, _ := r.store.UserByName(ctx, store.StarterUsername)
	if err := r.hub.Revoke(ctx, admin, status.Project, "stolen"); err != nil {
		t.Fatal(err)
	}
	if got := r.state(l, hublink.StateRevoked); got.Error != "stolen" {
		t.Errorf("revoked = %+v", got)
	}
	dials := r.dials.Load()
	time.Sleep(1500 * time.Millisecond)
	if got := r.dials.Load(); got != dials {
		t.Errorf("a revoked link dialed %d more times", got-dials)
	}
	// its own unlink has nothing to ask, and the record stays
	if state, err := l.Unlink(ctx); err != nil || state != hublink.StateRevoked {
		t.Errorf("unlink = %q, %v", state, err)
	}
	if reg, err := r.store.Registration(ctx, status.Project); err != nil || reg.State != hublink.StateRevoked {
		t.Errorf("the revocation = %+v, %v", reg, err)
	}
	// a code does not help until an operator forgets it, and then it does: the way back of a link revoked by mistake
	code := r.code()
	if got := register(t, l, code); got.State != hublink.StateRevoked {
		t.Errorf("a revoked link with a code = %+v", got)
	}
	if err := r.hub.Forget(ctx, admin, status.Project); err != nil {
		t.Fatal(err)
	}
	again := register(t, l, code)
	if again.State != hublink.StateApproved || again.Project == status.Project {
		t.Errorf("registered again = %+v", again)
	}
}

// stranger is a client that proved a key of its own, as anyone who reaches the endpoint can. It returns what the hub said.
func stranger(t *testing.T, address, code string) (*yamux.Session, hublink.Verdict) {
	t.Helper()
	session, _, verdict := proves(t, address, code)
	return session, verdict
}

// proves is stranger, with the control stream.
func proves(t *testing.T, address, code string) (*yamux.Session, *hublink.Lines, hublink.Verdict) {
	t.Helper()
	session, err := hublink.Dialer{Hub: address}.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	public, private, _ := ed25519.GenerateKey(nil)
	stream, err := session.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	control := hublink.NewLines(stream)
	if err := control.Write(hublink.Hello{Protocol: hublink.Version, Project: "web", Key: public}); err != nil {
		t.Fatal(err)
	}
	var greeting hublink.Greeting
	if err := control.Read(&greeting); err != nil {
		t.Fatal(err)
	}
	if err := control.Write(hublink.Proof{Signature: ed25519.Sign(private, hublink.Signed(address, greeting.Nonce, public, code)), Code: code}); err != nil {
		t.Fatal(err)
	}
	var verdict hublink.Verdict
	if err := control.Read(&verdict); err != nil {
		t.Fatalf("the verdict did not arrive before the close: %v", err)
	}
	return session, control, verdict
}

func TestAStrangerIsToldAndClosedAndCanPushNothing(t *testing.T) {
	r := newRig(t)
	for _, code := range []string{"", "egzo-link-wrong"} {
		session, verdict := stranger(t, r.linkURL, code)
		if verdict != (hublink.Verdict{State: hublink.StateUnregistered}) {
			t.Errorf("code %q: the stranger was told %+v", code, verdict)
		}
		select {
		case <-session.CloseChan():
		case <-time.After(3 * time.Second):
			t.Fatal("the hub held a stranger's connection")
		}
		if feed, err := session.OpenStream(); err == nil {
			_ = hublink.NewLines(feed).Write(hublink.FeedOpen{Feed: hublink.FeedStatus})
			var start hublink.FeedStart
			if hublink.NewLines(feed).Read(&start) == nil {
				t.Error("a stranger opened a feed")
			}
		}
	}
	if regs, _ := r.store.Registrations(ctx); len(regs) != 0 {
		t.Errorf("registrations = %+v", regs)
	}
	if entries, _ := r.store.AuditOf(ctx, "", 10); len(entries) != 0 {
		t.Errorf("audit = %+v", entries)
	}
}

func TestAVerdictSentRightBeforeTheCloseArrivesWhileTheLinkIsPushing(t *testing.T) {
	r := newRig(t)
	admin, _ := r.store.UserByName(ctx, store.StarterUsername)
	lost := 0
	const rounds = 30
	for range rounds {
		session, control, verdict := proves(t, r.linkURL, r.code())
		if verdict.State != hublink.StateApproved {
			t.Fatalf("registered = %+v", verdict)
		}
		feed, err := session.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		lines := hublink.NewLines(feed)
		if err := lines.Write(hublink.FeedOpen{Feed: hublink.FeedStatus}); err != nil {
			t.Fatal(err)
		}
		var start hublink.FeedStart
		if err := lines.Read(&start); err != nil {
			t.Fatal(err)
		}
		go func() {
			for lines.Write(hublink.StatusLine{Reason: strings.Repeat("x", 32<<10)}) == nil {
			}
		}()
		time.Sleep(5 * time.Millisecond)
		if err := r.hub.Revoke(ctx, admin, verdict.Project, "stolen"); err != nil {
			t.Fatal(err)
		}
		var last hublink.Verdict
		if err := control.Read(&last); err != nil {
			lost++
		} else if last.State != hublink.StateRevoked || last.Message != "stolen" {
			t.Errorf("the link heard %+v", last)
		}
		session.Close()
	}
	if lost != 0 {
		t.Errorf("%d of %d verdicts were lost", lost, rounds)
	}
}

func TestAFeedThatPushesFastIsHeldBackInBytes(t *testing.T) {
	r := newRig(t)
	session, verdict := stranger(t, r.linkURL, r.code())
	if verdict.State != hublink.StateApproved {
		t.Fatalf("registered = %+v", verdict)
	}
	feed, err := session.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	lines := hublink.NewLines(feed)
	if err := lines.Write(hublink.FeedOpen{Feed: hublink.FeedStatus}); err != nil {
		t.Fatal(err)
	}
	var start hublink.FeedStart
	if err := lines.Read(&start); err != nil {
		t.Fatal(err)
	}
	line := hublink.StatusLine{Reason: strings.Repeat("x", 1<<20)}
	began, sent := time.Now(), 0
	for time.Since(began) < 2*time.Second && sent < 64 {
		if err := lines.Write(line); err != nil {
			t.Fatalf("after %d MiB: %v", sent, err)
		}
		sent++
	}
	// a second's worth to spend at once, and what the stream holds, and the rest at the rate: what the hub takes in two seconds
	if sent >= 32 {
		t.Errorf("%d MiB were pushed in %v: nothing held them back", sent, time.Since(began))
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
	r.state(l, hublink.StateUnregistered)
	status := register(t, l, r.code())
	reg, err := r.store.Registration(ctx, status.Project)
	if err != nil {
		t.Fatal(err)
	}

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
	r.state(l, hublink.StateUnregistered) // the address with a slash is the same address

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		q.Host = strings.TrimPrefix(r.server.URL, "http://")
		r.server.Config.Handler.ServeHTTP(w, q)
	}))
	t.Cleanup(other.Close)
	liar, _ := dial(t, other.URL)
	r.state(liar, hublink.StateUnreachable)
}

func TestAProofIsForTheConfiguredAddressWhateverTheRequestSays(t *testing.T) {
	r := newRig(t)
	// the Host header is the client's to choose: a hub that passes a challenge on says its own name there, and it must not help
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { r.server.Config.Handler.ServeHTTP(w, q) }))
	t.Cleanup(other.Close)
	l, _ := dial(t, other.URL)
	r.state(l, hublink.StateUnreachable)
}

func TestAnAddressThatKeepsFailingIsToldToWait(t *testing.T) {
	r := newRig(t)
	for range 20 {
		stranger(t, r.linkURL, "egzo-link-wrong")
	}
	time.Sleep(100 * time.Millisecond) // the stranger hears its verdict before the hub has counted it
	session, err := hublink.Dialer{Hub: r.linkURL}.Dial(ctx)
	if err == nil {
		session.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("after 20 failures: %v", err)
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
	r.link("data", "coder")
	root := r.browser()
	root.signIn("admin")
	_, listed := root.get("/links")
	all := list(listed, "links")
	if len(all) != 2 {
		t.Fatalf("links = %v", all)
	}
	for _, one := range all {
		if one["state"] != hublink.StateApproved || one["owner"] != "admin" || one["code"] != nil {
			t.Errorf("link = %v", one)
		}
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
