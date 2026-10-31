package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var required = map[string]string{"EGZO_PUBLIC_URL": "https://hub.example.org"}

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
	if c.Listen != ":8080" || c.LinkListen != "" || c.DataDir != "/data" || c.LinkURL != "https://hub.example.org" {
		t.Errorf("%+v", c)
	}
	if !c.LocalLogin || c.AdminPassword != "" || c.OIDCIssuer != "" || c.OIDCName != "SSO" || len(c.OIDCScopes) != 0 {
		t.Errorf("without single sign-on, only the starter account: %+v", c)
	}
}

func TestTheIssuerIsKeptExactlyAndTheSecretIsOptional(t *testing.T) {
	c, err := Parse(env(with(map[string]string{"EGZO_OIDC_ISSUER": "https://tenant.auth0.com/", "EGZO_OIDC_CLIENT_ID": "hub",
		"EGZO_OIDC_SCOPES": "openid profile, email offline_access", "EGZO_OIDC_NAME": "Corp login"})))
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDCIssuer != "https://tenant.auth0.com/" {
		t.Errorf("the issuer was changed: %q", c.OIDCIssuer)
	}
	if strings.Join(c.OIDCScopes, " ") != "openid profile email offline_access" || c.OIDCName != "Corp login" || c.OIDCSecret != "" {
		t.Errorf("%+v", c)
	}
}

func TestEveryMissingSettingIsNamed(t *testing.T) {
	_, err := Parse(env(map[string]string{"EGZO_OIDC_ISSUER": "https://login.example.com"}))
	for _, name := range []string{"EGZO_PUBLIC_URL", "EGZO_LINK_URL", "EGZO_OIDC_CLIENT_ID"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s is not named: %v", name, err)
		}
	}
}

func TestLocalLogin(t *testing.T) {
	c, err := Parse(env(with(map[string]string{"EGZO_LOCAL_LOGIN": "false", "EGZO_OIDC_ISSUER": "https://login.example.com", "EGZO_OIDC_CLIENT_ID": "hub",
		"EGZO_ADMIN_PASSWORD": "pw"})))
	if err != nil || c.LocalLogin || c.AdminPassword != "pw" {
		t.Errorf("%+v %v", c, err)
	}
	if _, err := Parse(env(with(map[string]string{"EGZO_LOCAL_LOGIN": "false"}))); err == nil || !strings.Contains(err.Error(), "EGZO_OIDC_ISSUER") {
		t.Errorf("no local login and no single sign-on was accepted: %v", err)
	}
	if _, err := Parse(env(with(map[string]string{"EGZO_LOCAL_LOGIN": "maybe"}))); err == nil {
		t.Error("a bad EGZO_LOCAL_LOGIN was accepted")
	}
}

func TestOverrides(t *testing.T) {
	c, err := Parse(env(with(map[string]string{
		"EGZO_HUB_LISTEN": ":9000", "EGZO_LINK_LISTEN": ":9443", "EGZO_TRUSTED_PROXIES": "10.0.0.0/8, 172.18.0.2 ,", "EGZO_PUBLIC_URL": "https://hub.example.com",
		"EGZO_LINK_URL": "https://link.example.com/",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.LinkListen != ":9443" || len(c.TrustedProxy) != 2 || c.PublicURL != "https://hub.example.com" || c.LinkURL != "https://link.example.com" {
		t.Errorf("%+v", c)
	}
}

func TestTheAddressOfTheLinksIsTheAddressOfTheHubUnlessItHasItsOwn(t *testing.T) {
	c, err := Parse(env(map[string]string{"EGZO_LINK_URL": "http://10.0.0.2:8080"}))
	if err != nil || c.LinkURL != "http://10.0.0.2:8080" || c.PublicURL != "" {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []string{"hub.example.org", "ftp://hub.example.org", "https://hub.example.org/hub", "https://me:pw@hub.example.org", "https://"} {
		if _, err := Parse(env(map[string]string{"EGZO_LINK_URL": bad})); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
