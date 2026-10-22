// Package oidctest is a small OpenID Connect provider for tests and for the browser tests' server: a login page where whoever is
// testing types who they are. It signs real ID tokens, so the hub's verification is the real one.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Provider is the handler; set Issuer to the address it is served at. It is written verbatim into tokens, so an issuer with a
// trailing slash (as Auth0's) is one too. Change the knobs before the first request.
type Provider struct {
	Issuer       string
	ClientID     string
	ClientSecret string // empty: a public client, which must use PKCE

	// AuthMethods is what discovery lists as token_endpoint_auth_methods_supported; nil lists nothing. Only the listed methods are
	// accepted when it is set.
	AuthMethods []string
	// UserinfoOnly leaves every profile claim out of the id_token and answers them at the userinfo endpoint only.
	UserinfoOnly bool
	// UserinfoSubject, when set, is the sub the userinfo endpoint answers (a misbehaving provider).
	UserinfoSubject string
	// Profile gives the claims of a user beyond sub; by default user@example.com (verified), name and preferred_username user.
	Profile func(user string) map[string]any

	key *rsa.PrivateKey

	mu     sync.Mutex
	codes  map[string]grant
	tokens map[string]string // access token -> user
	// Challenges counts the authorization requests that carried an S256 PKCE challenge.
	Challenges int
}

type grant struct {
	user, nonce, redirect, challenge string
}

func (p *Provider) base() string { return strings.TrimSuffix(p.Issuer, "/") }

func (p *Provider) profile(user string) map[string]any {
	if p.Profile != nil {
		return p.Profile(user)
	}
	return map[string]any{"email": user + "@example.com", "email_verified": true, "name": user, "preferred_username": user}
}

func New(issuer, clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return &Provider{Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, key: key, codes: map[string]grant{}, tokens: map[string]string{}}
}

func (p *Provider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer": p.Issuer, "authorization_endpoint": p.base() + "/authorize", "token_endpoint": p.base() + "/token",
			"userinfo_endpoint": p.base() + "/userinfo", "jwks_uri": p.base() + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"code_challenge_methods_supported": []string{"S256"},
		}
		if p.AuthMethods != nil {
			meta["token_endpoint_auth_methods_supported"] = p.AuthMethods
		}
		json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("GET /userinfo", p.userinfo)
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("GET /authorize", p.authorizeForm)
	mux.HandleFunc("POST /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	return mux
}

var form = template.Must(template.New("login").Parse(`<!doctype html><title>Test login</title>
<h1>Sign in to the test provider</h1>
<form method="post" action="/authorize">
{{range $k, $v := .}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<label>Who are you? <input name="user" autofocus></label>
<button type="submit">Sign in</button>
</form>`))

func (p *Provider) authorizeForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID {
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	for _, k := range []string{"client_id", "redirect_uri", "state", "nonce", "response_type", "scope", "code_challenge", "code_challenge_method"} {
		fields[k] = q.Get(k)
	}
	form.Execute(w, fields)
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	user, redirect := r.Form.Get("user"), r.Form.Get("redirect_uri")
	if r.Form.Get("client_id") != p.ClientID || user == "" || redirect == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	challenge := ""
	if r.Form.Get("code_challenge_method") == "S256" {
		challenge = r.Form.Get("code_challenge")
	}
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	p.mu.Lock()
	p.codes[code] = grant{user: user, nonce: r.Form.Get("nonce"), redirect: redirect, challenge: challenge}
	if challenge != "" {
		p.Challenges++
	}
	p.mu.Unlock()
	target, _ := url.Parse(redirect)
	q := target.Query()
	q.Set("code", code)
	q.Set("state", r.Form.Get("state"))
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, basic := r.BasicAuth()
	method := "client_secret_basic"
	if !basic {
		id, secret, method = r.Form.Get("client_id"), r.Form.Get("client_secret"), "client_secret_post"
		if p.ClientSecret == "" {
			method = "none"
		}
	}
	if id != p.ClientID || secret != p.ClientSecret || (p.AuthMethods != nil && !contains(p.AuthMethods, method)) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	g, found := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	if !found || g.redirect != r.Form.Get("redirect_uri") {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	if g.challenge != "" || p.ClientSecret == "" {
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if g.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			http.Error(w, `{"error":"invalid_grant","error_description":"PKCE"}`, http.StatusBadRequest)
			return
		}
	}
	claims := map[string]any{"nonce": g.nonce}
	if !p.UserinfoOnly {
		for k, v := range p.profile(g.user) {
			claims[k] = v
		}
	}
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "test"}}, (&jose.SignerOptions{}).WithType("JWT"))
	token, _ := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: p.Issuer, Subject: "sub-" + g.user, Audience: jwt.Audience{p.ClientID},
		IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Claims(claims).Serialize()
	access := "at-" + Random()
	p.mu.Lock()
	p.tokens[access] = g.user
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "id_token": token})
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	user, ok := p.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims := map[string]any{"sub": "sub-" + user}
	if p.UserinfoSubject != "" {
		claims["sub"] = p.UserinfoSubject
	}
	for k, v := range p.profile(user) {
		claims[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(claims)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Random returns an unguessable token.
func Random() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
