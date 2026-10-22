// Package config reads the hub's settings from the environment, where the compose file puts them.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is everything the hub is told at start.
type Config struct {
	Listen       string
	DataDir      string
	SecretKey    string
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
	TrustedProxy  []string
	LocalEngine   string // the address of the engine seeded as "local"; empty for none
	// ScanInterval is how often an engine is listed again whatever its events said, in case one was missed. Changes are found by
	// the events; this is the fallback.
	ScanInterval time.Duration
}

// Parse reads the settings through get (os.Getenv in production). Every missing required setting is named in the one error.
func Parse(get func(string) string) (Config, error) {
	c := Config{
		Listen:        or(get("EGZO_HUB_LISTEN"), ":8080"),
		DataDir:       or(get("EGZO_HUB_DATA"), "/data"),
		SecretKey:     get("EGZO_SECRET_KEY"),
		OIDCIssuer:    get("EGZO_OIDC_ISSUER"), // exactly: the provider's issuer may end with a slash (Auth0's does)
		OIDCClientID:  get("EGZO_OIDC_CLIENT_ID"),
		OIDCSecret:    get("EGZO_OIDC_CLIENT_SECRET"),
		OIDCScopes:    strings.FieldsFunc(get("EGZO_OIDC_SCOPES"), func(r rune) bool { return r == ' ' || r == ',' }),
		OIDCName:      or(get("EGZO_OIDC_NAME"), "SSO"),
		LocalLogin:    true,
		AdminPassword: get("EGZO_ADMIN_PASSWORD"),
		PublicURL:     get("EGZO_PUBLIC_URL"),
		LocalEngine:   "unix:///var/run/docker.sock",
		ScanInterval:  60 * time.Second,
	}
	if v, ok := lookup(get, "EGZO_LOCAL_ENGINE"); ok {
		c.LocalEngine = v
		if v == "none" {
			c.LocalEngine = ""
		}
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
	if v := get("EGZO_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return c, fmt.Errorf("EGZO_SCAN_INTERVAL %q is not a duration of a second or more (like 10s)", v)
		}
		c.ScanInterval = d
	}
	required := []struct{ name, value string }{{"EGZO_SECRET_KEY", c.SecretKey}}
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
	return c, nil
}

func lookup(get func(string) string, name string) (string, bool) {
	v := get(name)
	return v, v != ""
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
