// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordDials makes the rig's transport note every address the proxy asks it to reach.
func recordDials(r *rig) func() []string {
	var mu sync.Mutex
	var addresses []string
	r.proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			addresses = append(addresses, address)
			mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, r.upstream.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: certPool(&CA{Cert: r.upstream.Certificate()})},
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), addresses...)
	}
}

// rawRequestThroughTermination sends raw request bytes to a terminated connection for example.com and
// returns the status of the answer.
func rawRequestThroughTermination(t *testing.T, r *rig, raw string) int {
	t.Helper()
	client, done := terminated(t, r, "example.com")
	client.SetDeadline(time.Now().Add(5 * time.Second))
	conn := tls.Client(client, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca)})
	io.WriteString(conn, raw)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		// the HTTP server may refuse a request so malformed that it answers nothing parseable
		client.Close()
		<-done
		return 0
	}
	response.Body.Close()
	client.Close()
	<-done
	return response.StatusCode
}

func TestRequestShapesInATerminatedConnectionOnlyEverReachExampleDotComOn443(t *testing.T) {
	for _, c := range []struct {
		name    string
		request string
		status  int
		dials   int
	}{
		{"trailing dot", "GET / HTTP/1.1\r\nHost: example.com.\r\nConnection: close\r\n\r\n", 200, 1},
		{"upper case and a port", "GET / HTTP/1.1\r\nHost: EXAMPLE.com:8080\r\nConnection: close\r\n\r\n", 200, 1},
		{"userinfo", "GET / HTTP/1.1\r\nHost: evil@example.com\r\nConnection: close\r\n\r\n", 400, 0},
		{"userinfo the other way", "GET / HTTP/1.1\r\nHost: example.com@other.example\r\nConnection: close\r\n\r\n", 400, 0},
		{"a path after the port", "GET / HTTP/1.1\r\nHost: example.com:443/x\r\nConnection: close\r\n\r\n", 400, 0},
		{"an IPv6 literal", "GET / HTTP/1.1\r\nHost: [::1]\r\nConnection: close\r\n\r\n", 421, 0},
		{"two dots", "GET / HTTP/1.1\r\nHost: example.com..\r\nConnection: close\r\n\r\n", 421, 0},
		{"duplicate Host", "GET / HTTP/1.1\r\nHost: example.com\r\nHost: other.example\r\nConnection: close\r\n\r\n", 400, 0},
		{"absolute URI for another host", "GET https://other.example/ HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n", 421, 0},
		{"another host in X-Forwarded-Host", "GET / HTTP/1.1\r\nHost: example.com\r\nX-Forwarded-Host: other.example\r\nConnection: close\r\n\r\n", 200, 1},
		{"another host", "GET / HTTP/1.1\r\nHost: other.example\r\nConnection: close\r\n\r\n", 421, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			dials := recordDials(r)
			if status := rawRequestThroughTermination(t, r, c.request); status != c.status {
				t.Errorf("status = %d, want %d", status, c.status)
			}
			got := dials()
			if len(got) != c.dials {
				t.Errorf("dials = %v, want %d of them", got, c.dials)
			}
			for _, address := range got {
				if address != "example.com:443" {
					t.Errorf("the proxy dialed %q", address)
				}
			}
		})
	}
}

func TestAnUpgradeRequestInATerminatedConnectionReachesOnlyTheConnectHost(t *testing.T) {
	upgrade := func(host string) string {
		return "GET /socket HTTP/1.1\r\nHost: " + host + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	}
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		io.WriteString(buffered, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buffered.Flush()
		conn.Close()
	})
	dials := recordDials(r)
	if status := rawRequestThroughTermination(t, r, upgrade("example.com")); status != http.StatusSwitchingProtocols {
		t.Errorf("status = %d, want 101", status)
	}
	if got := dials(); len(got) != 1 || got[0] != "example.com:443" {
		t.Errorf("dials = %v", got)
	}
	if status := rawRequestThroughTermination(t, r, upgrade("other.example")); status != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want 421", status)
	}
	if got := dials(); len(got) != 1 {
		t.Errorf("an upgrade for another host dialed: %v", got)
	}
}

func TestAnECHClientHelloAtAnInspectedHostIsTheExplicitPathAndIsNotLoggedAsTerminated(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, Service{Name: "docs", Hosts: []string{"example.com"}, Inspect: true})
	conn := r.tunnelTo("example.com")
	conn.Write(echHello("example.com", true))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !readServerHello(conn) {
		t.Fatal("no ServerHello")
	}
	conn.Close()
	response, err := r.client("coder", "tok", r.ca.Cert).Get("https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	waitFor(t, func() bool { return r.hasAction("example.com", "request") })
	if r.hasAction("example.com", "intercept") {
		t.Errorf("an inspected host was logged as a terminated ECH connection:\n%s", r.audit.String())
	}
	if !r.hasAction("example.com", "inspect") {
		t.Errorf("no inspect event:\n%s", r.audit.String())
	}
}

func TestAHelloThatRepeatsTheServerNameOrIsSplitOverTwoRecordsIsDeniedEvenWithECH(t *testing.T) {
	whole := echHello("example.com", true)
	payload := whole[5:]
	record := func(part []byte) []byte {
		return append([]byte{recordTypeHandshake, 3, 1, byte(len(part) >> 8), byte(len(part))}, part...)
	}
	duplicated := buildHello(serverNameExtension("example.com"), serverNameExtension("example.com"), extension(extensionECH, []byte{0, 1, 2, 3}))
	for name, hello := range map[string][]byte{
		"a repeated server name": record(duplicated),
		"two records":            append(record(payload[:40]), record(payload[40:])...),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			var dialed bool
			var mu sync.Mutex
			r.proxy.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				mu.Lock()
				dialed = true
				mu.Unlock()
				return nil, io.ErrClosedPipe
			}
			r.setPolicy("coder", "tok", []string{"example.com"})
			conn := r.tunnelTo("example.com")
			conn.Write(hello)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if n, _ := conn.Read(make([]byte, 1)); n != 0 {
				t.Error("the proxy answered a hello it should have refused")
			}
			waitFor(t, func() bool { return r.hasAction("example.com", "deny") })
			if r.hasAction("example.com", "intercept") || r.hasAction("example.com", "allow") {
				t.Errorf("the hello was served:\n%s", r.audit.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if dialed {
				t.Error("the upstream was dialed")
			}
		})
	}
}

// --- the handshake has a hard limit; idleness only counts after it --------------------------------------

func TestASlowHandshakeIsCutAtTheHandshakeTimeoutWhateverTheIdleTimeout(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.HandshakeTimeout = 300 * time.Millisecond
	r.proxy.IdleTimeout = time.Hour
	client, done := terminated(t, r, "example.com")
	hello := echHello("example.com", false)
	go func() { // a byte per 50ms: never idle for long, never done
		for _, b := range hello {
			if _, err := client.Write([]byte{b}); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a handshake that trickled in was still being served after the handshake timeout")
	}
	if !r.hasAction("example.com", "error") && !r.hasAction("example.com", "aborted") {
		t.Errorf("the failed handshake was not logged:\n%s", r.audit.String())
	}
}

func TestAQuietConnectionAfterTheHandshakeIsCutAtTheIdleTimeout(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.IdleTimeout = 300 * time.Millisecond
	client, done := terminated(t, r, "example.com")
	client.SetDeadline(time.Now().Add(10 * time.Second))
	conn := tls.Client(client, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca)})
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a quiet connection was never closed")
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("closed after %v", took)
	}
}

func TestAnIdleConnectionNeverOutlivesADeadlineSetFromOutside(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := newIdleConn(a, time.Hour)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	started := time.Now()
	if _, err := conn.Read(make([]byte, 1)); err == nil || time.Since(started) > 2*time.Second {
		t.Errorf("read returned %v after %v", err, time.Since(started))
	}
	conn.SetReadDeadline(time.Time{}) // cleared: the idle timeout alone applies again
	conn.idle = 100 * time.Millisecond
	started = time.Now()
	if _, err := conn.Read(make([]byte, 1)); err == nil || time.Since(started) > 2*time.Second {
		t.Errorf("read returned %v after %v", err, time.Since(started))
	}
}

// --- hosts are normalized once -------------------------------------------------------------------------

func TestAConnectHostWithATrailingDotIsTheSameHostAndIsLoggedNormalized(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	response, conn := connectRequest(t, r, "EXAMPLE.com.", "443")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	conn.Close()
	waitFor(t, func() bool { return r.hasAction("example.com", "aborted") || r.hasAction("example.com", "deny") })
	if strings.Contains(r.audit.String(), "example.com.") {
		t.Errorf("the audit holds the host unnormalized:\n%s", r.audit.String())
	}
}

func TestATrailingDotDoesNotReachAnotherHost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	for _, host := range []string{"other.example.", "example.com..", "example.com.other.example", "."} {
		if response, _ := connectRequest(t, r, host, "443"); response.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s: status = %d, want 403", host, response.StatusCode)
		}
	}
}

func TestRequestHost(t *testing.T) {
	for header, want := range map[string]string{
		"example.com": "example.com", "Example.COM.": "example.com", "example.com:443": "example.com", "[::1]": "::1", "[2001:DB8::1]:443": "2001:db8::1", "[::ffff:1.2.3.4]": "::ffff:1.2.3.4",
		"1.2.3.4:80": "1.2.3.4", "a_b.example": "a_b.example",
		"": "!", "evil@example.com": "!", "example.com@other": "!", "example.com:443/x": "!", "example.com:": "example.com",
		"example.com:80:80": "!", "a b": "!", ":443": "!", "example.com/x": "!", "[::1]@x": "!", "[::1": "!", "[]": "!", "[example.com]": "!", "[::1]x": "!", "[::1]:80x": "!", "[::g]": "!", "[::1]/x": "!",
	} {
		got, ok := requestHost(header)
		if !ok {
			got = "!"
		}
		if got != want {
			t.Errorf("requestHost(%q) = %q, want %q", header, got, want)
		}
	}
}

// --- authentication ------------------------------------------------------------------------------------

func TestAnUnknownAgentAndAWrongTokenAreRefusedTheSameWay(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	policy := r.proxy.policy.Load()
	for _, c := range []struct{ agent, token string }{
		{"coder", "wrong"}, {"coder", ""}, {"coder", "tokk"}, {"ghost", "tok"}, {"ghost", ""}, {"", ""},
	} {
		if r.proxy.authenticate(policy, c.agent, c.token) != nil {
			t.Errorf("%q with %q was let in", c.agent, c.token)
		}
	}
	if r.proxy.authenticate(policy, "coder", "tok") == nil {
		t.Error("the right credentials were refused")
	}
}
