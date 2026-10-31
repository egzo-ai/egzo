package publicurl

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(remote, host string, headers map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestForwardedHeadersAreBelievedOnlyFromAConfiguredProxy(t *testing.T) {
	resolver, err := New("", []string{"172.18.0.0/16", "10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "hub.example.com"}
	cases := []struct{ remote, want string }{
		{"172.18.0.3:5555", "https://hub.example.com"},
		{"10.0.0.5:1", "https://hub.example.com"},
		{"10.0.0.6:1", "http://hub:8080"},
		{"203.0.113.9:1", "http://hub:8080"},
		{"garbage", "http://hub:8080"},
	}
	for _, c := range cases {
		if got := resolver.Base(request(c.remote, "hub:8080", headers)); got != c.want {
			t.Errorf("from %s: %q, want %q", c.remote, got, c.want)
		}
	}
}

func TestWithoutProxiesTheHeadersAreIgnored(t *testing.T) {
	resolver, _ := New("", nil)
	got := resolver.Base(request("127.0.0.1:1", "localhost:8080", map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https"}))
	if got != "http://localhost:8080" {
		t.Errorf("got %q", got)
	}
}

func TestAChainOfProxiesUsesTheFirstValueAndABadHostIsIgnored(t *testing.T) {
	resolver, _ := New("", []string{"127.0.0.1"})
	got := resolver.Base(request("127.0.0.1:1", "hub", map[string]string{"X-Forwarded-Host": "a.example.com, internal", "X-Forwarded-Proto": "https, http"}))
	if got != "https://a.example.com" {
		t.Errorf("got %q", got)
	}
	got = resolver.Base(request("127.0.0.1:1", "hub", map[string]string{"X-Forwarded-Host": "evil.com/path?x", "X-Forwarded-Proto": "javascript"}))
	if got != "http://hub" {
		t.Errorf("a malformed forwarded host was used: %q", got)
	}
	for _, bad := range []string{"evil@hub.example.com", "a..example.com", "[::1]x", "hub.example.com:443:1"} {
		if got := resolver.Base(request("127.0.0.1:1", "hub", map[string]string{"X-Forwarded-Host": bad})); got != "http://hub" {
			t.Errorf("X-Forwarded-Host %q was used: %q", bad, got)
		}
	}
	if got := resolver.Base(request("127.0.0.1:1", "hub", map[string]string{"X-Forwarded-Host": "[2001:db8::1]:8443"})); got != "http://[2001:db8::1]:8443" {
		t.Errorf("an IPv6 literal with a port: %q", got)
	}
}

func TestAFixedPublicURLWinsAndMustBeABaseAddress(t *testing.T) {
	resolver, err := New("https://hub.example.com/", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolver.Base(request("127.0.0.1:1", "x", map[string]string{"X-Forwarded-Host": "other"})); got != "https://hub.example.com" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{"hub.example.com", "ftp://x", "https://x/path"} {
		if _, err := New(bad, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := New("", []string{"not-an-ip"}); err == nil {
		t.Error("a bad proxy address was accepted")
	}
}

func TestSecureFollowsTheScheme(t *testing.T) {
	resolver, _ := New("", []string{"127.0.0.1"})
	if !resolver.Secure(request("127.0.0.1:1", "h", map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Error("https behind a proxy is not secure")
	}
	direct := request("9.9.9.9:1", "h", nil)
	direct.TLS = &tls.ConnectionState{}
	if !resolver.Secure(direct) {
		t.Error("a TLS connection is not secure")
	}
	if resolver.Secure(request("9.9.9.9:1", "h", nil)) {
		t.Error("plain http is secure")
	}
}

func TestAWebSocketUpgradeHasTheSchemeOfThePage(t *testing.T) {
	resolver, _ := New("", []string{"127.0.0.0/8"})
	for proto, want := range map[string]string{"wss": "https://h", "ws": "http://h"} {
		if got := resolver.Base(request("127.0.0.1:1", "h", map[string]string{"X-Forwarded-Proto": proto})); got != want {
			t.Errorf("%s = %s", proto, got)
		}
	}
}
