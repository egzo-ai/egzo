// Package config reads the hub's settings from the environment, where the compose file puts them.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/egzo-ai/egzo/internal/names"
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
	// PreviewDomain is what previews live under (EGZO_PREVIEW_DOMAIN), with a port if people reach them on one; empty: no previews.
	// PreviewListen is where they are served (EGZO_PREVIEW_LISTEN, :8081 by default).
	PreviewDomain string
	PreviewListen string
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
		PreviewDomain: strings.ToLower(get("EGZO_PREVIEW_DOMAIN")),
		PreviewListen: or(get("EGZO_PREVIEW_LISTEN"), ":8081"),
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
	return c, c.checkPreviews()
}

// checkPreviews refuses a preview domain that would put code the agents wrote too close to the hub. Over https the hub's cookies are out of a
// sibling's reach; over http they cannot be, so a sibling is refused.
func (c Config) checkPreviews() error {
	if c.PreviewDomain == "" {
		return nil
	}
	if c.PublicURL == "" {
		return errors.New("EGZO_PREVIEW_DOMAIN needs EGZO_PUBLIC_URL: a preview sends people to the hub to sign in")
	}
	if _, ok := names.HostHeader(c.PreviewDomain); !ok || strings.Contains(c.PreviewDomain, "%") {
		return fmt.Errorf("EGZO_PREVIEW_DOMAIN %q is not a domain", c.PreviewDomain)
	}
	public, err := url.Parse(c.PublicURL)
	if err != nil {
		return fmt.Errorf("EGZO_PUBLIC_URL %q is not an address", c.PublicURL)
	}
	preview, _, found := strings.Cut(c.PreviewDomain, ":")
	if !found {
		preview = c.PreviewDomain
	}
	hub := strings.ToLower(public.Hostname())
	if preview == hub || strings.HasSuffix(hub, "."+preview) {
		return fmt.Errorf("EGZO_PREVIEW_DOMAIN %q holds the hub's own address: previews need a domain of their own", c.PreviewDomain)
	}
	if public.Scheme == "http" && sameSite(preview, hub) {
		return fmt.Errorf("EGZO_PREVIEW_DOMAIN %q is on the same site as the hub at %s: over http the hub's cookies cannot be kept from it. Use https, or a domain of its own", c.PreviewDomain, hub)
	}
	return nil
}

// sameSite says whether two hosts end alike. It does not know the public suffixes: at worst it refuses two sites under `.co.uk` on plain http,
// which is a trial, and a domain of its own is what it asks for anyway.
func sameSite(a, b string) bool {
	last := func(host string) string {
		labels := strings.Split(host, ".")
		return strings.Join(labels[max(0, len(labels)-2):], ".")
	}
	return last(a) == last(b)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
