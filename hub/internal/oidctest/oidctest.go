// Package oidctest is a small OpenID Connect provider for tests and for the browser tests' server: a login page where whoever is
// testing types who they are. It signs real ID tokens, so the hub's verification is the real one.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Provider is the handler; set Issuer to the address it is served at.
type Provider struct {
	Issuer       string
	ClientID     string
	ClientSecret string

	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	user, nonce, redirect string
}

func New(issuer, clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return &Provider{Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, key: key, codes: map[string]grant{}}
}

func (p *Provider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/authorize", "token_endpoint": p.Issuer + "/token",
			"jwks_uri": p.Issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
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
	for _, k := range []string{"client_id", "redirect_uri", "state", "nonce", "response_type", "scope"} {
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
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	p.mu.Lock()
	p.codes[code] = grant{user: user, nonce: r.Form.Get("nonce"), redirect: redirect}
	p.mu.Unlock()
	target, _ := url.Parse(redirect)
	q := target.Query()
	q.Set("code", code)
	q.Set("state", r.Form.Get("state"))
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		r.ParseForm()
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	p.mu.Lock()
	g, found := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	if !found || g.redirect != r.Form.Get("redirect_uri") {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "test"}}, (&jose.SignerOptions{}).WithType("JWT"))
	token, _ := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: p.Issuer, Subject: "sub-" + g.user, Audience: jwt.Audience{p.ClientID},
		IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Claims(map[string]any{"nonce": g.nonce, "email": g.user + "@example.com", "name": g.user}).Serialize()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + g.user, "token_type": "Bearer", "id_token": token})
}
