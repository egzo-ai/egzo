// Package config reads the hub's settings from the environment, where the compose file puts them.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config is everything the hub is told at start.
type Config struct {
	Listen       string
	DataDir      string
	SecretKey    string
	OIDCIssuer   string
	OIDCClientID string
	OIDCSecret   string
	PublicURL    string
	TrustedProxy []string
	LocalEngine  string // the address of the engine seeded as "local"; empty for none
	// ScanInterval is how often an engine is listed again whatever its events said, in case one was missed. Changes are found by
	// the events; this is the fallback.
	ScanInterval time.Duration
}

// Parse reads the settings through get (os.Getenv in production). Every missing required setting is named in the one error.
func Parse(get func(string) string) (Config, error) {
	c := Config{
		Listen:       or(get("EGZO_HUB_LISTEN"), ":8080"),
		DataDir:      or(get("EGZO_HUB_DATA"), "/data"),
		SecretKey:    get("EGZO_SECRET_KEY"),
		OIDCIssuer:   strings.TrimRight(get("EGZO_OIDC_ISSUER"), "/"),
		OIDCClientID: get("EGZO_OIDC_CLIENT_ID"),
		OIDCSecret:   get("EGZO_OIDC_CLIENT_SECRET"),
		PublicURL:    get("EGZO_PUBLIC_URL"),
		LocalEngine:  "unix:///var/run/docker.sock",
		ScanInterval: 60 * time.Second,
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
	if v := get("EGZO_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return c, fmt.Errorf("EGZO_SCAN_INTERVAL %q is not a duration of a second or more (like 10s)", v)
		}
		c.ScanInterval = d
	}
	var missing []string
	for _, r := range []struct{ name, value string }{
		{"EGZO_SECRET_KEY", c.SecretKey}, {"EGZO_OIDC_ISSUER", c.OIDCIssuer},
		{"EGZO_OIDC_CLIENT_ID", c.OIDCClientID}, {"EGZO_OIDC_CLIENT_SECRET", c.OIDCSecret},
	} {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return c, errors.New("missing settings: " + strings.Join(missing, ", "))
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
