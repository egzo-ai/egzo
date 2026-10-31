// Package publicurl works out the address users reach the hub at. The hub sits behind a TLS reverse proxy that is not part of
// egzo, so the scheme and host come from X-Forwarded-* headers, believed only when the request really comes from a configured proxy.
package publicurl

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/egzo-ai/egzo/internal/names"
)

// Resolver turns a request into the public base URL.
type Resolver struct {
	fixed   string
	proxies []netip.Prefix
}

// New builds a resolver. fixed, when not empty, is the address used always (EGZO_PUBLIC_URL). proxies are the CIDRs or addresses
// of the reverse proxies whose X-Forwarded-* headers are believed; none means the headers are ignored.
func New(fixed string, proxies []string) (*Resolver, error) {
	r := &Resolver{}
	if fixed != "" {
		u, err := url.Parse(fixed)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" && u.Path != "/" {
			return nil, fmt.Errorf("EGZO_PUBLIC_URL %q is not a base address like https://hub.example.com", fixed)
		}
		r.fixed = u.Scheme + "://" + u.Host
	}
	for _, p := range proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return nil, fmt.Errorf("trusted proxy %q is not an address or a CIDR", p)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		r.proxies = append(r.proxies, prefix)
	}
	return r, nil
}

func (r *Resolver) trusted(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range r.proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Base returns scheme://host for the request, without a trailing slash.
func (r *Resolver) Base(req *http.Request) string {
	if r.fixed != "" {
		return r.fixed
	}
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	host := req.Host
	if r.trusted(req.RemoteAddr) {
		// a proxy names the scheme of what it forwards, and for a WebSocket upgrade some (Traefik) say wss or ws
		switch first(req.Header.Get("X-Forwarded-Proto")) {
		case "https", "wss":
			scheme = "https"
		case "http", "ws":
			scheme = "http"
		}
		if v := first(req.Header.Get("X-Forwarded-Host")); validHost(v) {
			host = v
		}
	}
	return scheme + "://" + host
}

// Secure reports whether users reach the hub over https: cookies are marked Secure then.
func (r *Resolver) Secure(req *http.Request) bool { return strings.HasPrefix(r.Base(req), "https://") }

// first takes the first value of a list a chain of proxies built: the one the outermost proxy saw.
func first(v string) string {
	v, _, _ = strings.Cut(v, ",")
	return strings.TrimSpace(v)
}

// validHost reports whether h is what a Host header holds: a host and an optional port (names.HostHeader).
func validHost(h string) bool {
	_, ok := names.HostHeader(h)
	return ok
}
