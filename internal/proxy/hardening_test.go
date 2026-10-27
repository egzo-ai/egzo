// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitForASlot waits until the agent may open a connection again. Trying one takes a slot until it is
// released, so the probe runs until it succeeds once and is never repeated after that.
func waitForASlot(t *testing.T, r *rig) {
	t.Helper()
	opened := false
	waitFor(t, func() bool {
		if !opened {
			response, conn := connectRequest(t, r, "example.com", "443")
			conn.Close()
			opened = response.StatusCode == http.StatusOK
		}
		return opened
	})
}

func connectRequest(t *testing.T, r *rig, host, port string) (*http.Response, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	io.WriteString(conn, "CONNECT "+host+":"+port+" HTTP/1.1\r\nHost: "+host+":"+port+"\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return response, conn
}

// --- R-07: the proxy only reaches the public internet ---------------------------------------------------

func TestPublicAddresses(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.16.0.5": false, "172.31.255.255": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fc00::1": false, "0.0.0.0": false, "::": false, "100.64.0.1": false,
		"224.0.0.1": false, "ff02::1": false, "198.18.0.1": false, "240.0.0.1": false, "::ffff:10.0.0.1": false, "::ffff:127.0.0.1": false,
		"172.15.0.1": true, "172.32.0.1": true, "100.63.255.255": true,
	} {
		if got := isPublic(net.ParseIP(ip)); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestRefusesAnAddressLiteralThatIsNotPublicEvenWhenEverythingIsAllowed(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"*"})
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "[::1]"} {
		response, _ := connectRequest(t, r, host, "443")
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d", host, response.StatusCode)
		}
	}
	if !strings.Contains(r.audit.String(), "not a public address") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

func TestTheDefaultDialerRefusesNamesThatResolveToPrivateAddresses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := NewServer(mustCA(t), NewAudit(io.Discard))
	conn, err := server.dial(context.Background(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("the proxy's own dialer connected to a loopback address")
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("error = %v", err)
	}
}

func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// --- R-08: limits and idle timeouts ---------------------------------------------------------------------

func TestAnAgentCannotHoldMoreConnectionsThanTheLimit(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.MaxPerAgent = 2
	r.setPolicy("coder", "tok", []string{"example.com"})
	_, first := connectRequest(t, r, "example.com", "443")
	_, second := connectRequest(t, r, "example.com", "443")
	response, _ := connectRequest(t, r, "example.com", "443")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a third connection got status %d", response.StatusCode)
	}
	if !strings.Contains(r.audit.String(), "too many connections") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
	first.Close()
	second.Close()
	waitForASlot(t, r)
}

func TestTheLimitOfOneAgentDoesNotStopAnother(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.MaxPerAgent = 1
	r.proxy.SetPolicy(&Policy{Hash: "h", Profiles: map[string]Profile{"p": {Allow: []string{"example.com"}}}})
	r.proxy.BindAgent("coder", Binding{Instance: testInstance, Token: "tok", Profile: "p"})
	r.proxy.BindAgent("reviewer", Binding{Instance: testInstance, Token: "tok2", Profile: "p"})
	connectRequest(t, r, "example.com", "443")
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("reviewer:tok2"))
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("the other agent was refused: %v %v", response, err)
	}
}

func TestAnIdleTunnelIsClosed(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.IdleTimeout = 150 * time.Millisecond
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	handshake := tls.Client(conn, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true})
	if err := handshake.Handshake(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("an idle tunnel stayed open: %v", err)
	}
}

func TestAnIdleInterceptedConnectionIsClosed(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.IdleTimeout = 150 * time.Millisecond
	r.setPolicy("coder", "tok", nil, anthropic)
	conn := r.tunnelTo("example.com")
	roots := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca)})
	if err := roots.Handshake(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := roots.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("an idle intercepted connection stayed open: %v", err)
	}
}

func TestATunnelThatKeepsTalkingIsNotCutOffByTheIdleTimeout(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.IdleTimeout = 300 * time.Millisecond
	r.setPolicy("coder", "tok", nil, anthropic)
	client := r.client("coder", "tok", r.ca.Cert)
	for i := 0; i < 4; i++ {
		response, err := client.Get("https://example.com/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body(t, response)
		time.Sleep(150 * time.Millisecond)
	}
}

// --- R-09: bytes sent right behind CONNECT --------------------------------------------------------------

// pipelinedConn sends the CONNECT request together with the client's first TLS flight, without waiting
// for the 200, and hides the 200 from the TLS layer.
type pipelinedConn struct {
	net.Conn
	prefix  []byte
	reader  *bufio.Reader
	skipped bool
}

func (c *pipelinedConn) Write(p []byte) (int, error) {
	if c.prefix != nil {
		prefix := c.prefix
		c.prefix = nil
		if _, err := c.Conn.Write(append(prefix, p...)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *pipelinedConn) Read(p []byte) (int, error) {
	if !c.skipped {
		c.reader = bufio.NewReader(c.Conn)
		if _, err := http.ReadResponse(c.reader, nil); err != nil {
			return 0, err
		}
		c.skipped = true
	}
	return c.reader.Read(p)
}

func TestAClientHelloSentRightBehindCONNECTIsNotLost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	piped := &pipelinedConn{Conn: conn, prefix: []byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic " + auth + "\r\n\r\n")}
	client := tls.Client(piped, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca)})
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := client.Handshake(); err != nil {
		t.Fatalf("handshake with a pipelined ClientHello: %v", err)
	}
}

func TestATunnelledClientHelloSentRightBehindCONNECTIsNotLost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	piped := &pipelinedConn{Conn: conn, prefix: []byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic " + auth + "\r\n\r\n")}
	client := tls.Client(piped, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca), InsecureSkipVerify: true})
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := client.Handshake(); err != nil {
		t.Fatalf("handshake with a pipelined ClientHello through a tunnel: %v", err)
	}
}

// --- R-10: Encrypted Client Hello hides the real server name ---------------------------------------------

// hello builds a ClientHello record with the given extensions.
func buildHello(extensions ...[]byte) []byte {
	var body bytes.Buffer
	body.Write([]byte{3, 3})
	body.Write(make([]byte, 32))
	body.WriteByte(0) // session id
	binary.Write(&body, binary.BigEndian, uint16(2))
	body.Write([]byte{0x13, 0x01})
	body.Write([]byte{1, 0})
	var ext bytes.Buffer
	for _, e := range extensions {
		ext.Write(e)
	}
	binary.Write(&body, binary.BigEndian, uint16(ext.Len()))
	body.Write(ext.Bytes())
	var message bytes.Buffer
	message.WriteByte(handshakeClientHello)
	message.Write([]byte{0, byte(body.Len() >> 8), byte(body.Len())})
	message.Write(body.Bytes())
	return message.Bytes()
}

func extension(kind uint16, data []byte) []byte {
	out := make([]byte, 4, 4+len(data))
	binary.BigEndian.PutUint16(out, kind)
	binary.BigEndian.PutUint16(out[2:], uint16(len(data)))
	return append(out, data...)
}

func serverNameExtension(name string) []byte {
	entry := append([]byte{0, byte(len(name) >> 8), byte(len(name))}, name...)
	list := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	return extension(extensionServerName, list)
}

func TestParseServerNameFindsTheNameAmongOtherExtensions(t *testing.T) {
	hello := buildHello(extension(0x0010, []byte{0, 0}), serverNameExtension("example.com"), extension(0x002b, []byte{2, 3, 4}))
	if name, ech, err := parseServerName(hello); err != nil || name != "example.com" || ech {
		t.Errorf("name = %q, ech = %v, err = %v", name, ech, err)
	}
}

func TestParseServerNameReportsEncryptedClientHelloAndStillReturnsTheOuterName(t *testing.T) {
	for _, kind := range []uint16{0xfe08, 0xfe09, 0xfe0a, 0xfe0b, 0xfe0c, 0xfe0d, 0xff03, 0xffce} {
		// the name may come before or after the extension
		for _, hello := range [][]byte{
			buildHello(serverNameExtension("example.com"), extension(kind, []byte{0, 1, 2, 3})),
			buildHello(extension(kind, []byte{0, 1, 2, 3}), serverNameExtension("example.com")),
		} {
			if name, ech, err := parseServerName(hello); err != nil || name != "example.com" || !ech {
				t.Errorf("extension %#x: name = %q, ech = %v, err = %v", kind, name, ech, err)
			}
		}
		if name, ech, err := parseServerName(buildHello(extension(kind, nil))); err != nil || name != "" || !ech {
			t.Errorf("extension %#x without a name: name = %q, ech = %v, err = %v", kind, name, ech, err)
		}
	}
}

// echHello is a ClientHello record a TLS 1.3 server can answer, with the server name and the Encrypted
// Client Hello extension of the shape a browser sends as GREASE, as specs/fixtures/tls_client.py does.
func echHello(name string, ech bool) []byte {
	extensions := [][]byte{}
	if name != "" {
		extensions = append(extensions, serverNameExtension(name))
	}
	extensions = append(extensions,
		extension(0x000a, []byte{0, 2, 0, 0x1d}),
		extension(0x000d, []byte{0, 6, 4, 3, 8, 4, 8, 7}),
		extension(0x002b, []byte{2, 3, 4}),
		extension(0x0033, append([]byte{0, 36, 0, 0x1d, 0, 32}, bytes.Repeat([]byte{7}, 32)...)),
	)
	if ech {
		extensions = append(extensions, extension(extensionECH, bytes.Join([][]byte{{0, 0, 1, 0, 1, 9, 0, 32}, bytes.Repeat([]byte{5}, 32), {0, 176}, bytes.Repeat([]byte{6}, 176)}, nil)))
	}
	hello := buildHello(extensions...)
	return append([]byte{recordTypeHandshake, 3, 1, byte(len(hello) >> 8), byte(len(hello))}, hello...)
}

// readServerHello reads what a TLS server answers a hello with and reports whether it was a ServerHello.
func readServerHello(conn io.Reader) bool {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return false
	}
	return header[0] == recordTypeHandshake && header[5] == 0x02
}

// events parses the audit lines of one host.
func (r *rig) events(host string) []Event {
	var out []Event
	for _, line := range strings.Split(r.audit.String(), "\n") {
		var event Event
		if json.Unmarshal([]byte(line), &event) == nil && event.Host == host {
			out = append(out, event)
		}
	}
	return out
}

// steps names each event by its step, its decision and its mode: "connect/allow/intercept", "handshake/error", "close".
func steps(events []Event) []string {
	var out []string
	for _, event := range events {
		out = append(out, step(event))
	}
	return out
}

func step(event Event) string {
	name := string(event.Kind)
	for _, part := range []string{string(event.Decision), string(event.Mode)} {
		if part != "" {
			name += "/" + part
		}
	}
	return name
}

// hasStep says whether an event of host is the step (see steps), or starts with it.
func (r *rig) hasStep(host, prefix string) bool {
	for _, event := range r.events(host) {
		if strings.HasPrefix(step(event), prefix) {
			return true
		}
	}
	return false
}

func TestAnECHClientHelloForTheApprovedHostIsTerminatedAndTheUpstreamIsNeverDialed(t *testing.T) {
	r := newRig(t, nil)
	var dialed atomic.Bool
	r.proxy.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Store(true)
		return nil, io.ErrClosedPipe
	}
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	conn.Write(echHello("example.com", true))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !readServerHello(conn) {
		t.Fatal("the proxy did not answer with a ServerHello: the hello was refused or tunneled")
	}
	conn.Close()
	waitFor(t, func() bool { return r.hasStep("example.com", "close") })
	if dialed.Load() {
		t.Error("an ECH hello was tunneled: the upstream was dialed")
	}
	if r.requests() != 0 {
		t.Error("the upstream was contacted")
	}
	events := r.events("example.com")
	if got := steps(events); !slices.Equal(got, []string{"connect/allow/intercept", "handshake/error", "close"}) { // the client left after the ServerHello
		t.Fatalf("steps = %v", got)
	}
	if events[0].Reason.Code != CodeECH || !strings.Contains(events[0].Reason.Text, "Encrypted Client Hello") {
		t.Errorf("reason = %+v", events[0].Reason)
	}
	if events[0].Conn == "" || events[1].Conn != events[0].Conn || events[2].Conn != events[0].Conn {
		t.Errorf("the events of one connection have different ids: %v", events)
	}
	closed := events[len(events)-1]
	if closed.BytesUp < int64(len(echHello("example.com", true))) || closed.BytesDown == 0 {
		t.Errorf("close: up %d, down %d", closed.BytesUp, closed.BytesDown)
	}
}

func TestAnECHClientHelloSentRightBehindCONNECTIsTerminated(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	conn.Write(append([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n"), echHello("example.com", true)...))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	if response, err := http.ReadResponse(reader, nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", response, err)
	}
	if !readServerHello(reader) {
		t.Fatal("a hello sent right behind CONNECT was lost")
	}
}

func TestAHelloWithAnotherOrNoServerNameIsDeniedNeverTerminatedWithOrWithoutECH(t *testing.T) {
	for name, hello := range map[string][]byte{
		"other name":         echHello("other.example", true),
		"no name":            echHello("", true),
		"other name, no ECH": echHello("other.example", false),
		"no name, no ECH":    echHello("", false),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			var dialed atomic.Bool
			r.proxy.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				dialed.Store(true)
				return nil, io.ErrClosedPipe
			}
			r.setPolicy("coder", "tok", []string{"example.com"})
			conn := r.tunnelTo("example.com")
			conn.Write(hello)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if n, _ := conn.Read(make([]byte, 1)); n != 0 {
				t.Error("the proxy answered a hello with a name it did not approve")
			}
			waitFor(t, func() bool { return r.hasStep("example.com", "connect/deny") })
			for _, event := range r.events("example.com") {
				switch {
				case event.Decision == OutcomeDeny:
					if event.Reason.Code != CodeSNIMismatch || !strings.Contains(event.Reason.Text, "server name") {
						t.Errorf("reason = %+v", event.Reason)
					}
				case event.Decision == OutcomeAllow, event.Kind == KindClose:
					t.Errorf("unexpected %q event", step(event))
				}
			}
			if dialed.Load() {
				t.Error("the upstream was dialed")
			}
		})
	}
}

func TestAnECHClientHelloForAHostTheProfileDoesNotAllowIsRefusedAtCONNECT(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	response, _ := connectRequest(t, r, "other.example", "443")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if r.hasStep("other.example", "connect/allow") {
		t.Error("a host outside the profile was terminated")
	}
}

// terminated runs the ECH path of a connection (as the tunnel does once it saw the hello) over a pipe and
// returns the client side.
func terminated(t *testing.T, r *rig, host string) (client net.Conn, done chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done = make(chan struct{})
	go func() {
		defer close(done)
		c := newConn(r.proxy.audit, HTTPS, "10.0.0.2:40000")
		c.from("coder", testInstance, "coder")
		c.host = host
		r.proxy.terminate(server, c, Decision{}, true)
	}()
	t.Cleanup(func() { client.Close() })
	return client, done
}

func TestARequestInsideATerminatedConnectionIsForwardedUnloggedAndTheConnectionIsLoggedOnClose(t *testing.T) {
	r := newRig(t, nil)
	client, done := terminated(t, r, "example.com")
	web := &http.Client{Transport: &http.Transport{
		DialContext:     func(context.Context, string, string) (net.Conn, error) { return client, nil },
		TLSClientConfig: &tls.Config{RootCAs: certPool(r.ca)},
	}}
	response, err := web.Get("https://example.com/some/page?q=zq9query")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, response); got != "upstream says hello" {
		t.Errorf("body = %q", got)
	}
	web.CloseIdleConnections()
	client.Close()
	<-done
	log := r.audit.String()
	if strings.Contains(log, `"event":"request"`) || strings.Contains(log, "some/page") || strings.Contains(log, "zq9query") {
		t.Errorf("a terminated connection logged its requests:\n%s", log)
	}
	events := r.events("example.com")
	if got := steps(events); !slices.Equal(got, []string{"connect/allow/intercept", "close"}) {
		t.Fatalf("steps = %v", got)
	}
	if events[1].BytesUp == 0 || events[1].BytesDown == 0 {
		t.Errorf("close: up %d, down %d", events[1].BytesUp, events[1].BytesDown)
	}
}

func TestARequestForAnotherHostInsideATerminatedConnectionGets421(t *testing.T) {
	r := newRig(t, nil)
	client, done := terminated(t, r, "example.com")
	client.SetDeadline(time.Now().Add(5 * time.Second))
	conn := tls.Client(client, &tls.Config{ServerName: "example.com", RootCAs: certPool(r.ca)})
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: other.example\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d", response.StatusCode)
	}
	if r.requests() != 0 {
		t.Error("the upstream was contacted")
	}
	client.Close()
	<-done
}

func TestATerminatedConnectionVerifiesTheUpstreamCertificate(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, r.upstream.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: certPool(r.ca)}, // does not trust the upstream
	}
	client, done := terminated(t, r, "example.com")
	web := &http.Client{Transport: &http.Transport{
		DialContext:     func(context.Context, string, string) (net.Conn, error) { return client, nil },
		TLSClientConfig: &tls.Config{RootCAs: certPool(r.ca)},
	}}
	response, err := web.Get("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", response.StatusCode)
	}
	if r.requests() != 0 {
		t.Error("an unverified upstream was talked to")
	}
	client.Close()
	<-done
}

func TestAHandshakeFailureAfterECHSaysWhyAndAnInterceptedHostKeepsTheGenericMessage(t *testing.T) {
	r := newRig(t, nil)
	client, done := terminated(t, r, "example.com")
	client.SetDeadline(time.Now().Add(5 * time.Second))
	tls.Client(client, &tls.Config{ServerName: "example.com"}).Handshake() // does not trust the project CA
	client.Close()
	<-done
	var reason string
	for _, event := range r.events("example.com") {
		if event.Kind == KindHandshake && event.Decision == OutcomeError {
			reason = event.Reason.Text
		}
	}
	for _, want := range []string{"Encrypted Client Hello", "does not trust the project CA", "terminate TLS"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q lacks %q", reason, want)
		}
	}
	if !r.hasStep("example.com", "close") {
		t.Error("no close event")
	}

	r = newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	conn := r.tunnelTo("example.com")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	tls.Client(conn, &tls.Config{ServerName: "example.com"}).Handshake()
	conn.Close()
	waitFor(t, func() bool { return strings.Contains(r.audit.String(), "tls handshake") })
	if strings.Contains(r.audit.String(), "Encrypted Client Hello") {
		t.Errorf("the generic failure blames ECH:\n%s", r.audit.String())
	}
}

func TestATerminatedECHConnectionHoldsItsConnectionSlotUntilItEnds(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.MaxPerAgent = 1
	r.proxy.IdleTimeout = 300 * time.Millisecond
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	conn.Write(echHello("example.com", true))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !readServerHello(conn) {
		t.Fatal("no ServerHello")
	}
	if response, _ := connectRequest(t, r, "example.com", "443"); response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a second connection got status %d while the first was terminated", response.StatusCode)
	}
	// the client goes quiet: the idle timeout ends the connection, which frees the slot
	waitFor(t, func() bool { return r.hasStep("example.com", "close") })
	waitForASlot(t, r)
}

func TestParseServerNameHandlesFragmentsAndLongNamesWithoutPanicking(t *testing.T) {
	for _, good := range [][]byte{buildHello(serverNameExtension("example.com")), echHello("example.com", true)[5:]} {
		for i := 0; i < len(good); i++ {
			parseServerName(good[:i]) // must not panic on any truncation
		}
		for i := range good { // nor on a damaged byte
			damaged := append([]byte(nil), good...)
			damaged[i] ^= 0xff
			parseServerName(damaged)
		}
	}
	if _, _, err := parseServerName(buildHello(extension(extensionServerName, []byte{0xff, 0xff, 0, 0xff, 0xff}))); err == nil {
		t.Error("a server name list longer than its extension was accepted")
	}
	if _, _, err := parseServerName(buildHello(extension(extensionECH, nil), extension(extensionServerName, []byte{0xff, 0xff, 0, 0xff, 0xff}))); err == nil {
		t.Error("a damaged name next to an ECH extension was accepted")
	}
}

// --- R-11: the audit log is bounded and records how much went through ------------------------------------

func TestAuditFieldsFromTheAgentAreTruncated(t *testing.T) {
	var out syncBuffer
	audit := NewAudit(&out)
	audit.Log(Event{Agent: strings.Repeat("a", 5000), Host: strings.Repeat("h", 5000), Kind: KindConnect, Decision: OutcomeDeny, Reason: because(CodeNotInProfile, strings.Repeat("r", 5000))})
	line := out.String()
	if len(line) > 1500 {
		t.Fatalf("the audit line is %d bytes", len(line))
	}
	var event Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Agent) > 300 || !strings.HasSuffix(event.Agent, "…") {
		t.Errorf("agent = %d bytes %q", len(event.Agent), event.Agent[len(event.Agent)-5:])
	}
}

func TestAuditTruncationKeepsValidUTF8(t *testing.T) {
	var out syncBuffer
	NewAudit(&out).Log(Event{Kind: KindConnect, Decision: OutcomeDeny, Reason: because(CodeNotInProfile, strings.Repeat("é", 400))})
	if !json.Valid([]byte(out.String())) || strings.Contains(out.String(), "�") {
		t.Errorf("line = %q", out.String())
	}
}

func TestATunnelIsAuditedWhenItClosesWithTheBytesAndDuration(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	client := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: certPool(testUpstreamCA(r))})
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(client, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	io.Copy(io.Discard, client)
	conn.Close()
	waitFor(t, func() bool { return strings.Contains(r.audit.String(), `"event":"close"`) })
	var closed Event
	for _, line := range strings.Split(r.audit.String(), "\n") {
		if strings.Contains(line, `"event":"close"`) {
			json.Unmarshal([]byte(line), &closed)
		}
	}
	if closed.BytesUp == 0 || closed.BytesDown == 0 {
		t.Errorf("close event = %+v, want byte counts", closed)
	}
	if closed.Host != "example.com" || closed.Agent != "coder" {
		t.Errorf("close event = %+v", closed)
	}
}

// --- R-12: a proxy that has no policy says so ------------------------------------------------------------

func TestAProxyWithoutAPolicySaysSoAndHowToFixIt(t *testing.T) {
	r := newRig(t, nil)
	response, _ := connectRequest(t, r, "example.com", "443")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: not an authentication problem", response.StatusCode)
	}
	text := body(t, response)
	if !strings.Contains(text, "egzo project up") {
		t.Errorf("body = %q", text)
	}
	if !strings.Contains(r.audit.String(), "no egress policy") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

// --- R-13: the certificate cache is bounded --------------------------------------------------------------

func TestTheCertificateCacheIsBounded(t *testing.T) {
	minter := NewMinter(mustCA(t))
	minter.maxCache = 50
	for i := 0; i < 400; i++ {
		if _, err := minter.Certificate("host" + itoa(i) + ".example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if n := minter.size(); n > 50 {
		t.Errorf("cache holds %d certificates, want at most 50", n)
	}
}

func TestTheCacheDropsExpiredCertificatesFirst(t *testing.T) {
	minter := NewMinter(mustCA(t))
	minter.maxCache = 10
	now := time.Now()
	minter.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		minter.Certificate("old" + itoa(i) + ".example.com")
	}
	now = now.Add(48 * time.Hour)
	for i := 0; i < 3; i++ {
		minter.Certificate("new" + itoa(i) + ".example.com")
	}
	if n := minter.size(); n != 3 {
		t.Errorf("cache holds %d certificates, want only the 3 current ones", n)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// --- R-14: the CA on disk -------------------------------------------------------------------------------

// testKey is a secret key, as the policies and rotations of the operator tests carry it (keyed adds it to a body).
const testKey = "operator-test-secret-key-0123456789abcdef"

func keyed(body string) string {
	key := `{"project":"p","secret_key":"` + testKey + `"`
	if body == "{}" {
		return key + "}"
	}
	return strings.Replace(body, "{", key+",", 1)
}

// operatorFor is the operator API of a proxy whose store has been opened by a first policy, and the CA it opened.
func operatorFor(t *testing.T, cfg Config) (http.Handler, *Server, *CA) {
	t.Helper()
	store, err := OpenStore(cfg.CADir)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store.CA(), NewAudit(io.Discard))
	server.UseStore(store)
	handler := operatorHandler(server, store, cfg)
	if response := call(handler, "PUT", "/policy", keyed(`{"hash":"first","profiles":{}}`)); response.Code != http.StatusNoContent && !strings.Contains(cfg.PubDir, "blocker") {
		t.Fatalf("first policy: %d %s", response.Code, response.Body)
	}
	return handler, server, store.CA()
}

func call(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
	return recorder
}

func TestRotatingTheCAKeepsEverythingConsistentWhenPublishingFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("a file where a directory is needed"), 0o644)
	cfg := Config{CADir: filepath.Join(dir, "private"), PubDir: filepath.Join(blocker, "pub"), SystemBundle: "/nonexistent"}
	handler, server, ca := operatorFor(t, cfg)
	before := ca.Fingerprint()
	if response := call(handler, "POST", "/ca/rotate", keyed(`{}`)); response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	onDisk, err := OpenStore(cfg.CADir)
	if err != nil || onDisk.CA().Fingerprint() != before {
		t.Errorf("the CA on disk changed although publishing failed: %v", err)
	}
	if fingerprint := server.minter.ca.Fingerprint(); fingerprint != before {
		t.Error("the proxy signs with a CA that is not the one on disk")
	}
	var reported map[string]string
	json.Unmarshal(call(handler, "GET", "/policy", "").Body.Bytes(), &reported)
	if reported["ca"] != before {
		t.Errorf("reported CA = %s", reported["ca"])
	}
}

func TestRotatingTheCAPublishesAndSwitches(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CADir: filepath.Join(dir, "private"), PubDir: filepath.Join(dir, "pub"), SystemBundle: "/nonexistent"}
	handler, server, ca := operatorFor(t, cfg)
	var reply map[string]string
	response := call(handler, "POST", "/ca/rotate", keyed(`{}`))
	json.Unmarshal(response.Body.Bytes(), &reply)
	if response.Code != 200 || reply["ca"] == ca.Fingerprint() {
		t.Fatalf("rotate: %d %v", response.Code, reply)
	}
	published, _ := os.ReadFile(filepath.Join(cfg.PubDir, "ca.crt"))
	if !bytes.Equal(published, server.minter.ca.CertPEM) {
		t.Error("the published certificate is not the CA in use")
	}
}

func TestThePolicyEndpointRejectsBadAndOversizedBodies(t *testing.T) {
	handler, server, _ := operatorFor(t, Config{CADir: t.TempDir(), PubDir: t.TempDir(), SystemBundle: "/nonexistent"})
	for name, body := range map[string]string{
		"not json":     "{",
		"no hash":      keyed(`{"profiles":{}}`),
		"no key":       `{"hash":"x","project":"p","profiles":{}}`,
		"a short key":  `{"hash":"x","project":"p","secret_key":"short","profiles":{}}`,
		"oversized":    `{"hash":"x","profiles":{"a":{"allow":["` + strings.Repeat("a", 9<<20) + `"]}}}`,
		"wrong shapes": `{"hash":"x","profiles":[]}`,
	} {
		if response := call(handler, "PUT", "/policy", body); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, response.Code)
		}
	}
	if server.PolicyHash() != "first" {
		t.Error("a rejected policy was loaded")
	}
	if response := call(handler, "PUT", "/policy", keyed(`{"hash":"abc","profiles":{}}`)); response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	var reported map[string]string
	json.Unmarshal(call(handler, "GET", "/policy", "").Body.Bytes(), &reported)
	if reported["hash"] != "abc" {
		t.Errorf("reported = %v", reported)
	}
}

func TestTheOperatorBindsAndUnbindsAgentsByName(t *testing.T) {
	handler, server, _ := operatorFor(t, Config{CADir: t.TempDir(), PubDir: t.TempDir(), SystemBundle: "/nonexistent"})
	bind := `{"instance":"01JBX7Q0000000000000000000","token":"t","profile":"p"}`
	if response := call(handler, "PUT", "/agents/coder", bind); response.Code != http.StatusConflict {
		t.Errorf("binding before a policy was loaded: status = %d", response.Code)
	}
	if response := call(handler, "PUT", "/policy", keyed(`{"hash":"h","profiles":{"p":{"allow":["example.com"]}}}`)); response.Code != http.StatusNoContent {
		t.Fatalf("policy: status = %d", response.Code)
	}
	for name, c := range map[string]struct{ path, body string }{
		"a name with a slash": {"/agents/a%2Fb", bind},
		"an upper case name":  {"/agents/Coder", bind},
		"no token":            {"/agents/coder", `{"profile":"p"}`},
		"not json":            {"/agents/coder", `{`},
		"an unknown profile":  {"/agents/coder", `{"instance":"01JBX7Q0000000000000000000","token":"t","profile":"nope"}`},
		"an oversized body":   {"/agents/coder", `{"instance":"01JBX7Q0000000000000000000","token":"` + strings.Repeat("a", 70<<10) + `","profile":"p"}`},
	} {
		if response := call(handler, "PUT", c.path, c.body); response.Code < 400 {
			t.Errorf("%s: status = %d", name, response.Code)
		}
	}
	if len(server.BoundAgents()) != 0 {
		t.Fatalf("a rejected binding was kept: %v", server.BoundAgents())
	}
	if response := call(handler, "PUT", "/agents/coder", bind); response.Code != http.StatusNoContent {
		t.Fatalf("bind: status = %d", response.Code)
	}
	var listed []string
	json.Unmarshal(call(handler, "GET", "/agents", "").Body.Bytes(), &listed)
	if len(listed) != 1 || listed[0] != "coder" {
		t.Errorf("listed = %v", listed)
	}
	// Loading the policy again keeps the binding.
	call(handler, "PUT", "/policy", keyed(`{"hash":"h2","profiles":{"p":{"allow":["other.example"]}}}`))
	if len(server.BoundAgents()) != 1 {
		t.Errorf("a new policy dropped the bindings: %v", server.BoundAgents())
	}
	if response := call(handler, "DELETE", "/agents/coder", ""); response.Code != http.StatusNoContent {
		t.Errorf("unbind: status = %d", response.Code)
	}
	if response := call(handler, "DELETE", "/agents/coder", ""); response.Code != http.StatusNoContent {
		t.Errorf("unbinding twice: status = %d", response.Code)
	}
	if len(server.BoundAgents()) != 0 {
		t.Errorf("still bound: %v", server.BoundAgents())
	}
}
