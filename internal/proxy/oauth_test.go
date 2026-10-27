// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// provider is a token endpoint: it answers refreshes with what answer says, and counts them.
type provider struct {
	answer   func(form url.Values) (int, string)
	refreshs []url.Values
}

func (p *provider) refresh(_ context.Context, _ string, _ OAuth, form url.Values) (*http.Response, error) {
	p.refreshs = append(p.refreshs, form)
	status, body := p.answer(form)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func jwtWith(claims string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"RS256"}`)) + "." + encode([]byte(claims)) + ".c2lnbmF0dXJl"
}

// openSignIns are the sign-ins of a store opened with a key, in a directory of their own.
func openSignIns(t *testing.T, dir string, p *provider) *signIns {
	t.Helper()
	store := openStore(t, dir)
	if _, err := store.Unlock(sealKeysFor(t, "one")); err != nil {
		t.Fatal(err)
	}
	s := newSignIns(store)
	s.refresh = p.refresh
	if err := s.load(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAStandInKeepsTheClaimsOfAJWTAndIsRecognizedWithoutItsSignIn(t *testing.T) {
	real := jwtWith(`{"sub":"u","exp":2000000000}`)
	stand := standIn(real)
	if stand == real || !isStandIn(stand) || isStandIn(real) {
		t.Fatalf("stand-in %q of %q", stand, real)
	}
	if strings.Join(strings.Split(stand, ".")[:2], ".") != strings.Join(strings.Split(real, ".")[:2], ".") {
		t.Error("the stand-in of a JWT lost its header or claims")
	}
	if opaque := standIn("rt_opaque"); !isStandIn(opaque) || strings.Contains(opaque, "rt_opaque") {
		t.Errorf("opaque stand-in %q", opaque)
	}
	if isStandIn("someone-elses-token") || isStandIn("a.b.c") {
		t.Error("a token the proxy did not make was taken for a stand-in")
	}
}

func TestACapturedSignInSwapsItsStandInsRenewsAheadOfExpiryAndIsSealed(t *testing.T) {
	dir := t.TempDir()
	p := &provider{answer: func(url.Values) (int, string) {
		return 200, `{"access_token":"` + jwtWith(`{"sub":"u","n":2}`) + `","refresh_token":"rt-2","expires_in":3600}`
	}}
	s := openSignIns(t, dir, p)
	now := time.Now()
	s.now = func() time.Time { return now }
	held, err := s.capture("idp", "app_x", tokens{Access: jwtWith(`{"sub":"u","n":1}`), Refresh: "rt-1", ExpiresIn: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if !isStandIn(held.Access) || !isStandIn(held.Refresh) || held.Refresh == "rt-1" {
		t.Fatalf("the client got %+v", held)
	}
	oauth := OAuth{TokenURL: "https://idp.example/oauth/token"}
	if real, err := s.access(context.Background(), "idp", oauth, held.Access); err != nil || real != jwtWith(`{"sub":"u","n":1}`) {
		t.Fatalf("access = %q, %v", real, err)
	}
	if _, err := s.access(context.Background(), "idp", oauth, standIn("other")); !errors.Is(err, errNoSignIn) {
		t.Errorf("an unknown stand-in: %v", err)
	}

	now = now.Add(56 * time.Minute) // inside the renewal window
	s.renewDue(context.Background(), map[string]OAuth{"idp": oauth})
	if len(p.refreshs) != 1 || p.refreshs[0].Get("refresh_token") != "rt-1" || p.refreshs[0].Get("client_id") != "app_x" {
		t.Fatalf("renewals = %v", p.refreshs)
	}
	if real, _ := s.access(context.Background(), "idp", oauth, held.Access); real != jwtWith(`{"sub":"u","n":2}`) {
		t.Error("an older stand-in does not get the renewed token")
	}
	for _, entry := range mustReadDir(t, dir) {
		data, _ := os.ReadFile(dir + "/" + entry)
		if strings.Contains(string(data), "rt-2") || strings.Contains(string(data), "rt-1") {
			t.Errorf("%s holds a token in clear", entry)
		}
	}
	reopened := openSignIns(t, dir, p)
	reopened.now = s.now
	if real, err := reopened.access(context.Background(), "idp", oauth, held.Access); err != nil || real != jwtWith(`{"sub":"u","n":2}`) {
		t.Errorf("after a restart: %q, %v", real, err)
	}
	if reopened.byName["idp"].Refresh != "rt-2" {
		t.Error("the rotated refresh token was not the one kept")
	}
}

func TestAProviderThatRefusesEndsTheSignInAndAnErrorKeepsIt(t *testing.T) {
	status, body := 500, `{"error":"server_error"}`
	p := &provider{answer: func(url.Values) (int, string) { return status, body }}
	s := openSignIns(t, t.TempDir(), p)
	held, _ := s.capture("idp", "", tokens{Access: "at", Refresh: "rt", ExpiresIn: 1})
	now := time.Now().Add(time.Hour)
	s.now = func() time.Time { return now }
	oauth := OAuth{TokenURL: "https://idp.example/oauth/token"}
	s.renewDue(context.Background(), map[string]OAuth{"idp": oauth})
	if s.status("idp").State != "live" {
		t.Fatal("a server error ended the sign-in")
	}
	status, body = 400, `{"error":"invalid_grant","error_description":"refresh_token_reused"}`
	now = now.Add(time.Minute)
	if _, err := s.refreshFor(context.Background(), "idp", oauth, held.Refresh); !errors.Is(err, errNoSignIn) {
		t.Fatalf("refresh of a refused chain: %v", err)
	}
	if status := s.status("idp"); status.State != "ended" || status.Ended == "" {
		t.Errorf("status = %+v", status)
	}
	if _, err := s.access(context.Background(), "idp", oauth, held.Access); !errors.Is(err, errNoSignIn) {
		t.Errorf("the stand-in of an ended sign-in: %v", err)
	}
}

func TestTheTokenEndpointAnswersARefreshItselfAndNeverSendsAStandInUpstream(t *testing.T) {
	var upstream []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		upstream = append(upstream, string(data))
		io.WriteString(w, `{"access_token":"at-real","refresh_token":"rt-real","id_token":"`+jwtWith(`{"email":"e"}`)+`","expires_in":3600,"scope":"openid"}`)
	}))
	defer backend.Close()
	server := NewServer(mustCA(t), NewAudit(io.Discard))
	server.Transport = rewriteTo(backend.URL)
	server.signIns = openSignIns(t, t.TempDir(), &provider{answer: func(url.Values) (int, string) { return 500, "" }})
	oauth := OAuth{TokenURL: "https://idp.example/oauth/token"}
	post := func(body, contentType string) map[string]any {
		r := httptest.NewRequest("POST", "https://idp.example/oauth/token", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		server.serveToken(w, r, "idp", oauth)
		var answer map[string]any
		json.Unmarshal(w.Body.Bytes(), &answer)
		return answer
	}

	signedIn := post("grant_type=authorization_code&code=c&client_id=app", "application/x-www-form-urlencoded")
	if len(upstream) != 1 || signedIn["access_token"] == "at-real" || !isStandIn(signedIn["access_token"].(string)) || signedIn["scope"] != "openid" {
		t.Fatalf("the exchange: %v (upstream %v)", signedIn, upstream)
	}
	refreshed := post(`{"grant_type":"refresh_token","refresh_token":"`+signedIn["refresh_token"].(string)+`"}`, "application/json")
	if len(upstream) != 1 || refreshed["access_token"] != signedIn["access_token"] {
		t.Errorf("a refresh with a stand-in went upstream, or changed the stand-ins: %v", refreshed)
	}
	refused := post("grant_type=urn:ietf:params:oauth:grant-type:token-exchange&subject_token="+signedIn["refresh_token"].(string), "application/x-www-form-urlencoded")
	if refused["error"] != "invalid_grant" || len(upstream) != 1 {
		t.Errorf("a stand-in in another grant: %v", refused)
	}
}

// rewriteTo sends every request to base, whatever its URL says.
func rewriteTo(base string) http.RoundTripper {
	target, _ := url.Parse(base)
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
