package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/oidctest"
)

func TestALoginYieldsTheVerifiedIdentity(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	server := httptest.NewServer(provider.Handler())
	defer server.Close()
	provider.Issuer = server.URL
	o := auth.New(auth.Config{Issuer: server.URL, ClientID: "hub", ClientSecret: "secret"})
	redirect := "http://hub.example/auth/callback"

	authURL, err := o.AuthCodeURL(context.Background(), redirect, "the-state", "the-nonce")
	if err != nil {
		t.Fatal(err)
	}
	code := approve(t, authURL, "alice")
	claims, err := o.Complete(context.Background(), redirect, code, "the-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Identity != server.URL+"\nsub-alice" || claims.Email != "alice@example.com" || claims.Name != "alice" {
		t.Errorf("claims = %+v", claims)
	}
}

func approve(t *testing.T, authURL, user string) string {
	t.Helper()
	u, _ := url.Parse(authURL)
	q := u.Query()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(u.Scheme+"://"+u.Host+"/authorize", url.Values{
		"user": {user}, "client_id": {q.Get("client_id")}, "redirect_uri": {q.Get("redirect_uri")}, "state": {q.Get("state")}, "nonce": {q.Get("nonce")},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(response.Header.Get("Location"))
	if target.Query().Get("state") != q.Get("state") {
		t.Errorf("the state did not come back: %s", target)
	}
	return target.Query().Get("code")
}

func TestALoginForAnotherNonceOrACodeUsedTwiceIsRefused(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	server := httptest.NewServer(provider.Handler())
	defer server.Close()
	provider.Issuer = server.URL
	o := auth.New(auth.Config{Issuer: server.URL, ClientID: "hub", ClientSecret: "secret"})
	redirect := "http://hub.example/auth/callback"
	authURL, _ := o.AuthCodeURL(context.Background(), redirect, "s", "right-nonce")
	code := approve(t, authURL, "alice")
	if _, err := o.Complete(context.Background(), redirect, code, "wrong-nonce"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("a nonce mismatch: %v", err)
	}
	if _, err := o.Complete(context.Background(), redirect, code, "right-nonce"); err == nil {
		t.Error("a code was accepted twice")
	}
	if _, err := o.Complete(context.Background(), redirect, "forged", "right-nonce"); err == nil {
		t.Error("a forged code was accepted")
	}
}

func TestAWrongClientSecretIsRefused(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	server := httptest.NewServer(provider.Handler())
	defer server.Close()
	provider.Issuer = server.URL
	o := auth.New(auth.Config{Issuer: server.URL, ClientID: "hub", ClientSecret: "wrong"})
	authURL, _ := o.AuthCodeURL(context.Background(), "http://h/cb", "s", "n")
	code := approve(t, authURL, "alice")
	if _, err := o.Complete(context.Background(), "http://h/cb", code, "n"); err == nil {
		t.Error("the provider accepted a wrong client secret")
	}
}

func TestAnUnreachableProviderIsReportedAndRetried(t *testing.T) {
	o := auth.New(auth.Config{Issuer: "http://127.0.0.1:1", ClientID: "hub", ClientSecret: "s"})
	if _, err := o.AuthCodeURL(context.Background(), "http://h/cb", "s", "n"); err == nil || !strings.Contains(err.Error(), "login provider") {
		t.Errorf("err = %v", err)
	}
}

func TestRandomTokensDiffer(t *testing.T) {
	if auth.Random() == auth.Random() || len(auth.Random()) < 32 {
		t.Error("tokens repeat or are short")
	}
}
