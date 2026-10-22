// Package auth is the hub's sign-in: OpenID Connect against one issuer (any mainstream provider), the state kept between the
// redirect and the callback, and the starter account's password hashing.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config says how to talk to the issuer.
type Config struct {
	Issuer       string // exactly as the provider writes it: a trailing slash counts
	ClientID     string
	ClientSecret string // empty for a public client, which relies on PKCE
	// Scopes requested, "openid" included; "openid profile email" by default.
	Scopes []string
}

// Claims is what the hub keeps of a sign-in. Everything but Issuer and Subject may be empty, and is display-only.
type Claims struct {
	Issuer, Subject   string
	Email             string
	EmailVerified     bool
	Name              string
	PreferredUsername string
	Picture           string
}

// OIDC is a relying party. It discovers the issuer on first use and again after a failure, so the hub can start before the
// provider is up.
type OIDC struct {
	cfg Config

	mu       sync.Mutex
	provider *oidc.Provider
	style    oauth2.AuthStyle
}

func New(cfg Config) *OIDC {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if !slices.Contains(cfg.Scopes, oidc.ScopeOpenID) {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	return &OIDC{cfg: cfg}
}

func (o *OIDC) discover(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, o.cfg.Issuer)
	if err != nil {
		if strings.Contains(err.Error(), "did not match the issuer") {
			return nil, fmt.Errorf("the login provider's issuer is not EGZO_OIDC_ISSUER, which must match it exactly, trailing slash and scheme included: %w", err)
		}
		return nil, fmt.Errorf("cannot reach the login provider %s: %w", o.cfg.Issuer, err)
	}
	var meta struct {
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	}
	_ = p.Claims(&meta)
	o.provider, o.style = p, authStyle(o.cfg.ClientSecret, meta.AuthMethods)
	return p, nil
}

// authStyle picks how the client authenticates to the token endpoint from what the provider lists: client_secret_basic when it
// is listed or nothing is (the default the spec gives), else client_secret_post. A public client sends only its id, in the form.
func authStyle(secret string, methods []string) oauth2.AuthStyle {
	switch {
	case secret == "":
		return oauth2.AuthStyleInParams
	case len(methods) == 0 || slices.Contains(methods, "client_secret_basic"):
		return oauth2.AuthStyleInHeader
	case slices.Contains(methods, "client_secret_post"):
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

func (o *OIDC) oauth(p *oidc.Provider, redirect string) *oauth2.Config {
	endpoint := p.Endpoint()
	endpoint.AuthStyle = o.style
	return &oauth2.Config{ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret, Endpoint: endpoint, RedirectURL: redirect, Scopes: o.cfg.Scopes}
}

// AuthCodeURL is where to send the browser to sign in. verifier is the PKCE code verifier, kept until the callback.
func (o *OIDC) AuthCodeURL(ctx context.Context, redirect, state, nonce, verifier string) (string, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return "", err
	}
	return o.oauth(p, redirect).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// NewVerifier makes a PKCE code verifier.
func NewVerifier() string { return oauth2.GenerateVerifier() }

// Complete trades the code for the verified identity of the user. Claims the id_token leaves out are read from the userinfo
// endpoint, when the provider has one.
func (o *OIDC) Complete(ctx context.Context, redirect, code, nonce, verifier string) (Claims, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return Claims{}, err
	}
	cfg := o.oauth(p, redirect)
	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("the provider refused the code: %w", err)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return Claims{}, errors.New("the provider returned no id_token")
	}
	id, err := p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("the id_token is not valid: %w", err)
	}
	if id.Nonce != nonce {
		return Claims{}, errors.New("the id_token answers another login (nonce)")
	}
	var c profileClaims
	if err := id.Claims(&c); err != nil {
		return Claims{}, err
	}
	if c.incomplete() && p.UserInfoEndpoint() != "" {
		if info, err := p.UserInfo(ctx, cfg.TokenSource(ctx, token)); err == nil {
			// the spec: userinfo's sub must be the id_token's, or its claims are not about this user
			if info.Subject == id.Subject {
				var more profileClaims
				if info.Claims(&more) == nil {
					c.fill(more)
				}
			}
		}
	}
	return Claims{Issuer: id.Issuer, Subject: id.Subject, Email: c.Email, EmailVerified: bool(c.EmailVerified), Name: c.Name,
		PreferredUsername: c.PreferredUsername, Picture: c.Picture}, nil
}

type profileClaims struct {
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Picture           string   `json:"picture"`
}

func (c profileClaims) incomplete() bool {
	return c.Email == "" || c.Name == "" || c.PreferredUsername == ""
}

func (c *profileClaims) fill(more profileClaims) {
	if c.Email == "" {
		c.Email, c.EmailVerified = more.Email, more.EmailVerified
	}
	if c.Name == "" {
		c.Name = more.Name
	}
	if c.PreferredUsername == "" {
		c.PreferredUsername = more.PreferredUsername
	}
	if c.Picture == "" {
		c.Picture = more.Picture
	}
}

// flexBool reads a boolean claim some providers send as a string ("true").
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*b = flexBool(x)
	case string:
		*b = flexBool(x == "true")
	}
	return nil
}

// Random returns an unguessable token for state and nonce.
func Random() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
