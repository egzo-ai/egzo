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

	verifier := auth.NewVerifier()
	authURL, err := o.AuthCodeURL(context.Background(), redirect, "the-state", "the-nonce", verifier)
	if err != nil {
		t.Fatal(err)
	}
	code := approve(t, authURL, "alice")
	claims, err := o.Complete(context.Background(), redirect, code, "the-nonce", verifier)
	if err != nil {
		t.Fatal(err)
	}
	want := auth.Claims{Issuer: server.URL, Subject: "sub-alice", Email: "alice@example.com", EmailVerified: true, Name: "alice", PreferredUsername: "alice"}
	if claims != want {
		t.Errorf("claims = %+v", claims)
	}
	if provider.Challenges != 1 {
		t.Error("the authorization request carried no PKCE challenge")
	}
}

// login runs a whole sign-in against provider as user.
func login(t *testing.T, o *auth.OIDC, user string) (auth.Claims, error) {
	t.Helper()
	verifier := auth.NewVerifier()
	authURL, err := o.AuthCodeURL(context.Background(), "http://hub.example/auth/callback", "s", "n", verifier)
	if err != nil {
		return auth.Claims{}, err
	}
	return o.Complete(context.Background(), "http://hub.example/auth/callback", approve(t, authURL, user), "n", verifier)
}

func serve(t *testing.T, provider *oidctest.Provider, path string) string {
	t.Helper()
	server := httptest.NewServer(provider.Handler())
	t.Cleanup(server.Close)
	provider.Issuer = server.URL + path
	return provider.Issuer
}

func TestAWrongVerifierIsRefused(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	issuer := serve(t, provider, "")
	o := auth.New(auth.Config{Issuer: issuer, ClientID: "hub", ClientSecret: "secret"})
	authURL, _ := o.AuthCodeURL(context.Background(), "http://h/cb", "s", "n", auth.NewVerifier())
	if _, err := o.Complete(context.Background(), "http://h/cb", approve(t, authURL, "alice"), "n", auth.NewVerifier()); err == nil {
		t.Error("a code was traded with another verifier")
	}
}

func TestAPublicClientSignsInWithPKCEAlone(t *testing.T) {
	provider := oidctest.New("", "hub", "")
	o := auth.New(auth.Config{Issuer: serve(t, provider, ""), ClientID: "hub"})
	if claims, err := login(t, o, "alice"); err != nil || claims.Subject != "sub-alice" {
		t.Errorf("claims = %+v, %v", claims, err)
	}
}

func TestTheClientAuthenticatesTheWayTheProviderLists(t *testing.T) {
	for _, methods := range [][]string{{"client_secret_post"}, {"client_secret_basic"}, {"client_secret_basic", "client_secret_post"}, nil} {
		provider := oidctest.New("", "hub", "secret")
		provider.AuthMethods = methods
		o := auth.New(auth.Config{Issuer: serve(t, provider, ""), ClientID: "hub", ClientSecret: "secret"})
		if _, err := login(t, o, "alice"); err != nil {
			t.Errorf("methods %v: %v", methods, err)
		}
	}
}

func TestClaimsMissingFromTheIDTokenAreReadFromUserinfo(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	provider.UserinfoOnly = true
	provider.Profile = func(user string) map[string]any {
		return map[string]any{"email": user + "@corp.com", "email_verified": "true", "name": "Alice A", "picture": "https://img.example/a.png"}
	}
	o := auth.New(auth.Config{Issuer: serve(t, provider, ""), ClientID: "hub", ClientSecret: "secret"})
	claims, err := login(t, o, "alice")
	if err != nil || claims.Email != "alice@corp.com" || !claims.EmailVerified || claims.Name != "Alice A" || claims.Picture != "https://img.example/a.png" || claims.PreferredUsername != "" {
		t.Errorf("claims = %+v, %v", claims, err)
	}
}

func TestUserinfoAboutAnotherSubjectIsIgnored(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	provider.UserinfoOnly = true
	provider.UserinfoSubject = "sub-mallory"
	o := auth.New(auth.Config{Issuer: serve(t, provider, ""), ClientID: "hub", ClientSecret: "secret"})
	claims, err := login(t, o, "alice")
	if err != nil || claims.Subject != "sub-alice" || claims.Email != "" {
		t.Errorf("claims = %+v, %v", claims, err)
	}
}

func TestAnIssuerWithATrailingSlashIsMatchedExactly(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	issuer := serve(t, provider, "/") // Auth0's issuers end with a slash
	if claims, err := login(t, auth.New(auth.Config{Issuer: issuer, ClientID: "hub", ClientSecret: "secret"}), "alice"); err != nil || claims.Issuer != issuer {
		t.Errorf("claims = %+v, %v", claims, err)
	}
	_, err := login(t, auth.New(auth.Config{Issuer: strings.TrimSuffix(issuer, "/"), ClientID: "hub", ClientSecret: "secret"}), "alice")
	if err == nil || !strings.Contains(err.Error(), "match it exactly") || !strings.Contains(err.Error(), issuer) {
		t.Errorf("a mismatched issuer: %v", err)
	}
}

func approve(t *testing.T, authURL, user string) string {
	t.Helper()
	u, _ := url.Parse(authURL)
	q := u.Query()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(u.Scheme+"://"+u.Host+"/authorize", url.Values{
		"user": {user}, "client_id": {q.Get("client_id")}, "redirect_uri": {q.Get("redirect_uri")}, "state": {q.Get("state")}, "nonce": {q.Get("nonce")},
		"code_challenge": {q.Get("code_challenge")}, "code_challenge_method": {q.Get("code_challenge_method")},
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
	verifier := auth.NewVerifier()
	authURL, _ := o.AuthCodeURL(context.Background(), redirect, "s", "right-nonce", verifier)
	code := approve(t, authURL, "alice")
	if _, err := o.Complete(context.Background(), redirect, code, "wrong-nonce", verifier); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("a nonce mismatch: %v", err)
	}
	if _, err := o.Complete(context.Background(), redirect, code, "right-nonce", verifier); err == nil {
		t.Error("a code was accepted twice")
	}
	if _, err := o.Complete(context.Background(), redirect, "forged", "right-nonce", verifier); err == nil {
		t.Error("a forged code was accepted")
	}
}

func TestAWrongClientSecretIsRefused(t *testing.T) {
	provider := oidctest.New("", "hub", "secret")
	server := httptest.NewServer(provider.Handler())
	defer server.Close()
	provider.Issuer = server.URL
	o := auth.New(auth.Config{Issuer: server.URL, ClientID: "hub", ClientSecret: "wrong"})
	verifier := auth.NewVerifier()
	authURL, _ := o.AuthCodeURL(context.Background(), "http://h/cb", "s", "n", verifier)
	code := approve(t, authURL, "alice")
	if _, err := o.Complete(context.Background(), "http://h/cb", code, "n", verifier); err == nil {
		t.Error("the provider accepted a wrong client secret")
	}
}

func TestAnUnreachableProviderIsReportedAndRetried(t *testing.T) {
	o := auth.New(auth.Config{Issuer: "http://127.0.0.1:1", ClientID: "hub", ClientSecret: "s"})
	if _, err := o.AuthCodeURL(context.Background(), "http://h/cb", "s", "n", "v"); err == nil || !strings.Contains(err.Error(), "login provider") {
		t.Errorf("err = %v", err)
	}
}

func TestRandomTokensDiffer(t *testing.T) {
	a, b := auth.Random(), auth.Random()
	if a == b || len(a) < 32 {
		t.Error("tokens repeat or are short")
	}
}
