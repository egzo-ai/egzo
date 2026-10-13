// Package auth is the hub's login: OpenID Connect against one issuer (the bundled Dex, or any provider), and the state kept between
// the redirect and the callback. The hub never sees a password.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config says how to talk to the issuer.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// Scopes beyond "openid"; profile and email by default.
	Scopes []string
}

// Claims is what the hub keeps of a login.
type Claims struct {
	// Identity is the issuer and the subject together: a subject is only unique within its issuer.
	Identity string
	Email    string
	Name     string
}

// OIDC is a relying party. It discovers the issuer on first use and again after a failure, so the hub can start before Dex is up.
type OIDC struct {
	cfg Config

	mu       sync.Mutex
	provider *oidc.Provider
}

func New(cfg Config) *OIDC {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"profile", "email"}
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
		return nil, fmt.Errorf("cannot reach the login provider %s: %w", o.cfg.Issuer, err)
	}
	o.provider = p
	return p, nil
}

func (o *OIDC) oauth(p *oidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: redirect,
		Scopes: append([]string{oidc.ScopeOpenID}, o.cfg.Scopes...),
	}
}

// AuthCodeURL is where to send the browser to log in.
func (o *OIDC) AuthCodeURL(ctx context.Context, redirect, state, nonce string) (string, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return "", err
	}
	return o.oauth(p, redirect).AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

// Complete trades the code for the verified identity of the user.
func (o *OIDC) Complete(ctx context.Context, redirect, code, nonce string) (Claims, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return Claims{}, err
	}
	token, err := o.oauth(p, redirect).Exchange(ctx, code)
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
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		User  string `json:"preferred_username"`
	}
	if err := id.Claims(&claims); err != nil {
		return Claims{}, err
	}
	name := claims.Name
	if name == "" {
		name = claims.User
	}
	return Claims{Identity: id.Issuer + "\n" + id.Subject, Email: claims.Email, Name: name}, nil
}

// Random returns an unguessable token for state and nonce.
func Random() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
