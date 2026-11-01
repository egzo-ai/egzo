// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/preview"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

const (
	previewDomain = "egzo-preview.test"
	previewID     = "abcdefghijklmnop"
)

// previewRig is a hub with a project that exposes a port, and the preview listener next to it.
type previewRig struct {
	*rig
	front   *httptest.Server
	fake    *fake.Engine
	project string
	host    string
	seen    chan http.Header
	visitor string // who opens the previews: admin owns the project
}

func newPreviewRig(t *testing.T) *previewRig {
	t.Helper()
	r := newRigWith(t, []func(*rig){func(r *rig) { r.previews = previewDomain }})
	r.hub.ServePreviews("http", previewDomain)
	f := r.link("web", "coder")
	f.SetAgent(links.Agent{Name: "coder-1", State: "running"})
	p := &previewRig{rig: r, fake: f, project: r.id("web"), seen: make(chan http.Header, 16)}
	visitor, _, err := r.store.RegisterLocal(ctx, r.invite(store.Invite{Project: p.project, Role: rbac.Viewer}), "visitor", "hash", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p.visitor = visitor.ID
	p.grant(rbac.None) // a spec gives what it is about
	p.host = previewID + "-" + p.project + "." + previewDomain
	f.Listen("coder-1", 3000, http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		select {
		case p.seen <- q.Header.Clone():
		default:
		}
		switch q.URL.Path {
		case "/plant":
			w.Header().Add("Set-Cookie", "egzo_preview=planted; Path=/")
			w.Header().Add("Set-Cookie", "theirs=1")
		case "/stream":
			w.(http.Flusher).Flush()
			<-q.Context().Done()
			return
		}
		io.WriteString(w, "the app at "+q.URL.Path)
	}))
	f.Expose("coder-1", links.Exposure{Port: 3000, Protocol: hublink.ProtocolHTTP, Where: hublink.WhereHub, ID: previewID})
	p.front = httptest.NewServer(preview.New(r.hub.Previews(), r.app.PreviewGate(), preview.Options{Domain: previewDomain, Scheme: "http"}, nil))
	t.Cleanup(p.front.Close)
	r.waitFor("the exposure", func() bool { _, err := r.hub.Previews().Find(ctx, p.project, previewID); return err == nil })
	return p
}

func (p *previewRig) visit(path string, cookies ...*http.Cookie) *http.Response {
	p.t.Helper()
	req, _ := http.NewRequest("GET", p.front.URL+path, nil)
	req.Host = p.host
	for _, c := range cookies {
		req.AddCookie(c)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// logout signs out from a page of the hub.
func (p *previewRig) logout(session *http.Cookie) {
	p.t.Helper()
	req, _ := http.NewRequest("POST", p.server.URL+"/api/v1/logout", nil)
	req.Header.Set("Origin", p.server.URL)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatal(resp.Status)
	}
}

// session is the visitor signed in to the hub.
func (p *previewRig) session() *http.Cookie { return p.sessionOf(p.visitor) }

func (p *previewRig) sessionOf(user string) *http.Cookie {
	p.t.Helper()
	token, err := p.store.NewSession(ctx, user, store.MethodPassword, "", time.Hour)
	if err != nil {
		p.t.Fatal(err)
	}
	return &http.Cookie{Name: "egzo_session", Value: token}
}

func (p *previewRig) grant(role rbac.Role) {
	p.t.Helper()
	if err := p.store.SetRole(ctx, p.project, p.visitor, role); err != nil {
		p.t.Fatal(err)
	}
}

// signIn goes the way of the redirects, and returns the preview's cookie.
func (p *previewRig) signIn(session *http.Cookie, path string) *http.Cookie {
	p.t.Helper()
	ticket := p.ticket(session, path)
	entered := p.visit("/_egzo/enter?ticket=" + ticket)
	if entered.StatusCode != http.StatusSeeOther || entered.Header.Get("Location") != path || len(entered.Cookies()) != 1 {
		p.t.Fatalf("enter = %d %s %v", entered.StatusCode, entered.Header.Get("Location"), entered.Cookies())
	}
	return entered.Cookies()[0]
}

func (p *previewRig) ticket(session *http.Cookie, path string) string {
	p.t.Helper()
	opened := p.send("GET", "/preview/"+p.host+"?to="+url.QueryEscape(path), "", session)
	location, err := url.Parse(opened.Header.Get("Location"))
	if err != nil || opened.StatusCode != http.StatusSeeOther || location.Host != p.host || location.Path != "/_egzo/enter" {
		p.t.Fatalf("the hub answered %d %q", opened.StatusCode, opened.Header.Get("Location"))
	}
	return location.Query().Get("ticket")
}

func TestAPreviewIsNeverOpenWithoutSigningIn(t *testing.T) {
	p := newPreviewRig(t)
	sent := p.visit("/shop?x=1")
	location := sent.Header.Get("Location")
	if sent.StatusCode != http.StatusFound || !strings.Contains(location, "/preview/"+p.host+"?to=%2Fshop%3Fx%3D1") {
		t.Fatalf("%d %s", sent.StatusCode, location)
	}
	if calls := strings.Join(p.fake.Calls(), " "); strings.Contains(calls, "dial") {
		t.Errorf("the agent was reached: %s", calls)
	}
	hub := p.send("GET", "/preview/"+p.host+"?to=%2Fshop", "")
	if hub.StatusCode != http.StatusSeeOther || !strings.HasPrefix(hub.Header.Get("Location"), "/login?next=%2Fpreview%2F") {
		t.Errorf("%d %s", hub.StatusCode, hub.Header.Get("Location"))
	}
}

func TestTheRedirectsEndOnThePathFirstAskedForAndTheAppNeverSeesTheCookie(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	cookie := p.signIn(p.session(), "/shop?x=1")
	if cookie.Name != "egzo_preview" || !cookie.HttpOnly || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("%+v", cookie)
	}
	resp := p.visit("/shop", cookie, &http.Cookie{Name: "theirs", Value: "1"})
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(body) != "the app at /shop" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := (<-p.seen).Get("Cookie"); got != "theirs=1" {
		t.Errorf("the app saw %q", got)
	}
}

func TestAnAddressElsewhereIsNoPlaceToGoAfterSigningIn(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	session := p.session()
	ticket := p.ticket(session, "//evil.example.org/")
	if got := p.visit("/_egzo/enter?ticket=" + ticket).Header.Get("Location"); got != "/" {
		t.Error(got)
	}
}

func TestATicketWorksOnceAndOnItsOwnPreview(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	ticket := p.ticket(p.session(), "/")
	other := p.host
	p.host = "qqqqqqqqqqqqqqqq-" + p.project + "." + previewDomain
	if got := p.visit("/_egzo/enter?ticket=" + ticket).StatusCode; got != http.StatusForbidden {
		t.Errorf("on another preview: %d", got)
	}
	p.host = other
	if got := p.visit("/_egzo/enter?ticket=" + ticket); got.StatusCode != http.StatusSeeOther {
		t.Errorf("on its own: %d", got.StatusCode)
	}
	if got := p.visit("/_egzo/enter?ticket=" + ticket).StatusCode; got != http.StatusForbidden {
		t.Errorf("twice: %d", got)
	}
	if got := p.visit("/_egzo/enter?ticket=nonsense").StatusCode; got != http.StatusForbidden {
		t.Errorf("a made-up one: %d", got)
	}
}

func TestACookieIsForOnePreview(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	p.fake.Expose("coder-1", links.Exposure{Port: 3000, Protocol: hublink.ProtocolHTTP, Where: hublink.WhereHub, ID: "qqqqqqqqqqqqqqqq"})
	cookie := p.signIn(p.session(), "/")
	p.host = "qqqqqqqqqqqqqqqq-" + p.project + "." + previewDomain
	if got := p.visit("/", cookie).StatusCode; got != http.StatusFound {
		t.Errorf("%d", got)
	}
}

func TestWhoMayOpenAPreview(t *testing.T) {
	for role, opens := range map[rbac.Role]bool{rbac.Viewer: true, rbac.User: true, rbac.Admin: true, rbac.Owner: true, rbac.None: false} {
		t.Run("role "+string(role), func(t *testing.T) {
			p := newPreviewRig(t)
			if role != rbac.None {
				p.grant(role)
			}
			got := p.send("GET", "/preview/"+p.host, "", p.session()).StatusCode
			if want := map[bool]int{true: http.StatusSeeOther, false: http.StatusNotFound}[opens]; got != want {
				t.Errorf("%d, want %d", got, want)
			}
		})
	}
}

func TestAnOperatorWithNoRoleOverseesAndDoesNotOpen(t *testing.T) {
	p := newPreviewRig(t)
	id := p.userID("admin")
	if err := p.store.SetRole(ctx, p.project, p.visitor, rbac.Owner); err != nil {
		t.Fatal(err)
	}
	if err := p.store.SetRole(ctx, p.project, id, rbac.None); err != nil {
		t.Fatal(err)
	}
	if got := p.send("GET", "/preview/"+p.host, "", p.sessionOf(id)).StatusCode; got != http.StatusNotFound {
		t.Error(got)
	}
}

func TestTheAuditLogHasOneEntryPerTicket(t *testing.T) {
	p := newPreviewRig(t)
	refused := func() int {
		entries, _ := p.store.AuditOf(ctx, p.project, 100)
		n := 0
		for _, e := range entries {
			if e.Action == "preview.open" && !e.Allowed {
				n++
			}
		}
		return n
	}
	session := p.session()
	p.send("GET", "/preview/"+p.host, "", session)
	if refused() != 1 {
		t.Errorf("%d refusals", refused())
	}
	p.grant(rbac.Viewer)
	cookie := p.signIn(session, "/")
	p.visit("/", cookie)
	p.visit("/two", cookie)
	entries, _ := p.store.AuditOf(ctx, p.project, 100)
	opened := 0
	for _, e := range entries {
		if e.Action == "preview.open" && e.Allowed {
			opened++
			if e.Agent != "coder-1" || e.Detail != "port 3000" {
				t.Errorf("%+v", e)
			}
		}
	}
	if opened != 1 {
		t.Errorf("%d entries", opened)
	}
}

func TestEveryRequestIsCheckedAgain(t *testing.T) {
	t.Run("a role taken away", func(t *testing.T) {
		p := newPreviewRig(t)
		p.grant(rbac.Viewer)
		cookie := p.signIn(p.session(), "/")
		p.grant(rbac.None)
		if got := p.visit("/", cookie).StatusCode; got != http.StatusFound {
			t.Errorf("%d", got)
		}
	})
	t.Run("signing out", func(t *testing.T) {
		p := newPreviewRig(t)
		p.grant(rbac.Viewer)
		session := p.session()
		cookie := p.signIn(session, "/")
		p.logout(session)
		if got := p.visit("/", cookie).StatusCode; got != http.StatusFound {
			t.Errorf("%d", got)
		}
	})
	t.Run("a port no longer exposed", func(t *testing.T) {
		p := newPreviewRig(t)
		p.grant(rbac.Viewer)
		cookie := p.signIn(p.session(), "/")
		p.fake.Unexpose("coder-1", 3000)
		p.waitFor("a 404", func() bool { return p.visit("/", cookie).StatusCode == http.StatusNotFound })
	})
}

func TestAnAppCannotSetTheCookie(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	cookie := p.signIn(p.session(), "/")
	got := p.visit("/plant", cookie).Header.Values("Set-Cookie")
	if len(got) != 1 || got[0] != "theirs=1" {
		t.Errorf("%v", got)
	}
}

func TestWhatStaysOpenEndsWithTheRight(t *testing.T) {
	for name, take := range map[string]func(*previewRig, *http.Cookie){
		"the role": func(p *previewRig, _ *http.Cookie) {
			admin, err := p.store.User(ctx, p.userID("admin"))
			if err == nil {
				err = p.hub.SetRole(ctx, admin, p.project, p.visitor, rbac.None)
			}
			if err != nil {
				p.t.Fatal(err)
			}
		},
		"the port":    func(p *previewRig, _ *http.Cookie) { p.fake.Unexpose("coder-1", 3000) },
		"the session": func(p *previewRig, s *http.Cookie) { p.logout(s) },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPreviewRig(t)
			p.grant(rbac.Viewer)
			session := p.session()
			cookie := p.signIn(session, "/")
			stream := p.visit("/stream", cookie)
			ended := make(chan struct{})
			go func() { io.Copy(io.Discard, stream.Body); close(ended) }()
			take(p, session)
			select {
			case <-ended:
			case <-time.After(5 * time.Second):
				t.Error("it stayed open")
			}
		})
	}
}

func TestAnOfflineProjectSaysSoToWhoMayKnow(t *testing.T) {
	p := newPreviewRig(t)
	p.grant(rbac.Viewer)
	cookie := p.signIn(p.session(), "/")
	p.fake.Drop()
	p.waitFor("the page to say so", func() bool { return p.visit("/", cookie).StatusCode == http.StatusServiceUnavailable })
}
