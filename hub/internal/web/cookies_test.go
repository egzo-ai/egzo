// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/store"
)

const publicHub = "https://hub.example.com"

// httpsRig is a hub whose public address is https, as behind its TLS proxy. Requests carry their cookies by hand: a jar
// would not send a Secure cookie to the test's plain http server.
func httpsRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, []func(*rig){func(r *rig) { r.public = publicHub }})
}

// send makes a request with exactly the cookies given, and does not follow a redirect.
func (r *rig) send(method, path, body string, cookies ...*http.Cookie) *http.Response {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.server.URL+path, strings.NewReader(body))
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", publicHub)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (r *rig) passwordCookie() *http.Cookie {
	r.t.Helper()
	resp := r.send("POST", "/auth/login", `{"username":"admin","password":"`+adminPassword+`"}`)
	if len(resp.Cookies()) != 1 {
		r.t.Fatalf("sign-in = %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
	return resp.Cookies()[0]
}

func TestCookiesOverHTTPSAreHostOnly(t *testing.T) {
	r := httpsRig(t)
	session := r.passwordCookie()
	login := r.send("GET", "/login/start", "").Cookies()
	if len(login) != 1 {
		t.Fatalf("login cookies: %v", login)
	}
	for name, c := range map[string]*http.Cookie{"__Host-egzo_session": session, "__Host-egzo_login": login[0]} {
		if c.Name != name || !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly {
			t.Errorf("a browser would refuse %s, or another host could set it: %+v", name, c)
		}
	}
}

func TestCookiesOverPlainHTTPKeepTheirNames(t *testing.T) {
	r := newRig(t)
	b := r.browser()
	b.signIn("admin")
	resp := r.send("GET", "/login/start", "")
	if len(resp.Cookies()) != 1 || resp.Cookies()[0].Name != "egzo_login" || resp.Cookies()[0].Secure {
		t.Errorf("login cookie: %v", resp.Cookies())
	}
	if code, _ := b.get("/session"); code != 200 {
		t.Errorf("a session over http = %d", code)
	}
}

func TestAPlantedUnprefixedCookieIsNotAccepted(t *testing.T) {
	r := httpsRig(t)
	session := r.passwordCookie()
	// a page on a sibling host can set this for the parent domain, with a value that is a real session of its own
	planted := &http.Cookie{Name: "egzo_session", Value: session.Value}

	if resp := r.send("GET", "/api/v1/session", "", planted); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the planted cookie alone = %d", resp.StatusCode)
	}
	mine := &http.Cookie{Name: session.Name, Value: session.Value}
	if resp := r.send("GET", "/api/v1/session", "", &http.Cookie{Name: "egzo_session", Value: "someone-else"}, mine); resp.StatusCode != http.StatusOK {
		t.Errorf("the real cookie after a planted one = %d", resp.StatusCode)
	}
	// signing out reads the same cookie: a planted one must neither end a session nor hide the real one
	if resp := r.send("POST", "/api/v1/logout", "{}", planted); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign out = %d", resp.StatusCode)
	}
	if resp := r.send("GET", "/api/v1/session", "", mine); resp.StatusCode != http.StatusOK {
		t.Errorf("a planted cookie ended the session: %d", resp.StatusCode)
	}
}

func TestAPlantedLoginCookieDoesNotFinishASignIn(t *testing.T) {
	r := httpsRig(t)
	login := r.send("GET", "/login/start", "").Cookies()[0]
	planted := &http.Cookie{Name: "egzo_login", Value: login.Value}
	resp := r.send("GET", "/auth/callback?state=x", "", planted)
	if location := resp.Header.Get("Location"); !strings.Contains(location, "did+not+start+here") {
		t.Errorf("the callback took a planted login cookie: %s", location)
	}
}

func TestSigningOutEndsTheSessionOfTheCookieFromBeforeThePrefix(t *testing.T) {
	r := httpsRig(t)
	session, former := r.passwordCookie(), r.passwordCookie()
	unprefixed := &http.Cookie{Name: "egzo_session", Value: former.Value}
	resp := r.send("POST", "/api/v1/logout", "{}", unprefixed, session)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign out = %d", resp.StatusCode)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		cleared = cleared || c.Name == "egzo_session" && c.MaxAge < 0 && c.Secure
	}
	if !cleared {
		t.Errorf("the old cookie stays in the browser: %v", resp.Cookies())
	}
	if resp := r.send("GET", "/api/v1/session", "", former); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the old session survived signing out: %d", resp.StatusCode)
	}
}

func TestSigningOutLeavesThePlantedSessionOfAnotherUser(t *testing.T) {
	r := httpsRig(t)
	session := r.passwordCookie()
	other, _, _, err := r.store.LoginIdentity(ctx, "https://idp.example.com", "someone", store.Profile{PreferredUsername: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := r.store.NewSession(ctx, other.ID, store.MethodPassword, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	planted := &http.Cookie{Name: "egzo_session", Value: token}
	if resp := r.send("POST", "/api/v1/logout", "{}", planted, session); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign out = %d", resp.StatusCode)
	}
	if resp := r.send("GET", "/api/v1/session", "", &http.Cookie{Name: session.Name, Value: token}); resp.StatusCode != http.StatusOK {
		t.Errorf("signing out ended somebody else's session: %d", resp.StatusCode)
	}
}
