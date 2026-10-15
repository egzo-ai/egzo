package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var required = map[string]string{
	"EGZO_SECRET_KEY": "0123456789abcdef", "EGZO_OIDC_ISSUER": "http://dex:5556/dex/", "EGZO_OIDC_CLIENT_ID": "hub", "EGZO_OIDC_CLIENT_SECRET": "s",
}

func with(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range required {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestDefaults(t *testing.T) {
	c, err := Parse(env(required))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.DataDir != "/data" || c.LocalEngine != "unix:///var/run/docker.sock" || c.ScanInterval != 60*time.Second {
		t.Errorf("%+v", c)
	}
	if c.OIDCIssuer != "http://dex:5556/dex" {
		t.Errorf("the trailing slash of the issuer was kept: %q", c.OIDCIssuer)
	}
}

func TestEveryMissingSettingIsNamed(t *testing.T) {
	_, err := Parse(env(map[string]string{}))
	for _, name := range []string{"EGZO_SECRET_KEY", "EGZO_OIDC_ISSUER", "EGZO_OIDC_CLIENT_ID", "EGZO_OIDC_CLIENT_SECRET"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s is not named: %v", name, err)
		}
	}
}

func TestOverrides(t *testing.T) {
	c, err := Parse(env(with(map[string]string{
		"EGZO_HUB_LISTEN": ":9000", "EGZO_LOCAL_ENGINE": "none", "EGZO_TRUSTED_PROXIES": "10.0.0.0/8, 172.18.0.2 ,", "EGZO_SCAN_INTERVAL": "30s", "EGZO_PUBLIC_URL": "https://hub.example.com",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.LocalEngine != "" || len(c.TrustedProxy) != 2 || c.ScanInterval != 30*time.Second || c.PublicURL != "https://hub.example.com" {
		t.Errorf("%+v", c)
	}
	if _, err := Parse(env(with(map[string]string{"EGZO_SCAN_INTERVAL": "soon"}))); err == nil {
		t.Error("a bad interval was accepted")
	}
}
