// Package config reads the hub's settings from the environment, where the compose file puts them.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Config is everything the hub is told at start.
type Config struct {
	Listen string
	// LinkListen is a listener for the link endpoint alone (EGZO_LINK_LISTEN); empty: it is on Listen, with the UI.
	LinkListen   string
	DataDir      string
	OIDCIssuer   string // empty: no single sign-on, only the starter account
	OIDCClientID string
	OIDCSecret   string   // empty: a public client, with PKCE
	OIDCScopes   []string // empty: openid profile email
	OIDCName     string   // what the sign-in button says
	// LocalLogin is the starter account's password sign-in (EGZO_LOCAL_LOGIN, true by default). AdminPassword, when set, is its
	// password, saved at every start.
	LocalLogin    bool
	AdminPassword string
	PublicURL     string
	// LinkURL is the address projects name in `hub:`, which is what their proof is for: EGZO_LINK_URL, else EGZO_PUBLIC_URL.
	LinkURL      string
	TrustedProxy []string
}

// Parse reads the settings through get (os.Getenv in production). Every missing required setting is named in the one error.
func Parse(get func(string) string) (Config, error) {
	c := Config{
		Listen:        or(get("EGZO_HUB_LISTEN"), ":8080"),
		LinkListen:    get("EGZO_LINK_LISTEN"),
		DataDir:       or(get("EGZO_HUB_DATA"), "/data"),
		OIDCIssuer:    get("EGZO_OIDC_ISSUER"), // exactly: the provider's issuer may end with a slash (Auth0's does)
		OIDCClientID:  get("EGZO_OIDC_CLIENT_ID"),
		OIDCSecret:    get("EGZO_OIDC_CLIENT_SECRET"),
		OIDCScopes:    strings.FieldsFunc(get("EGZO_OIDC_SCOPES"), func(r rune) bool { return r == ' ' || r == ',' }),
		OIDCName:      or(get("EGZO_OIDC_NAME"), "SSO"),
		LocalLogin:    true,
		AdminPassword: get("EGZO_ADMIN_PASSWORD"),
		PublicURL:     get("EGZO_PUBLIC_URL"),
		LinkURL:       or(get("EGZO_LINK_URL"), get("EGZO_PUBLIC_URL")),
	}
	for _, p := range strings.Split(get("EGZO_TRUSTED_PROXIES"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxy = append(c.TrustedProxy, p)
		}
	}
	if v := get("EGZO_LOCAL_LOGIN"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("EGZO_LOCAL_LOGIN %q is not true or false", v)
		}
		c.LocalLogin = on
	}
	required := []struct{ name, value string }{{"EGZO_PUBLIC_URL (or EGZO_LINK_URL), the address projects name in hub:", c.LinkURL}}
	if c.OIDCIssuer != "" {
		required = append(required, struct{ name, value string }{"EGZO_OIDC_CLIENT_ID", c.OIDCClientID})
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return c, errors.New("missing settings: " + strings.Join(missing, ", "))
	}
	if !c.LocalLogin && c.OIDCIssuer == "" {
		return c, errors.New("EGZO_LOCAL_LOGIN=false needs single sign-on (EGZO_OIDC_ISSUER): without either, nobody can sign in")
	}
	u, err := url.Parse(c.LinkURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("the address projects name in hub: %q is not a base address like https://hub.example.com", c.LinkURL)
	}
	c.LinkURL = u.Scheme + "://" + u.Host // as core keeps `hub:`
	return c, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
