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
	"strconv"
	"strings"
	"testing"
	"time"
)

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
	conn, err := server.Dial(context.Background(), "tcp", listener.Addr().String())
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
	ca, err := LoadOrCreateCA(t.TempDir())
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
	waitFor(t, func() bool {
		response, conn := connectRequest(t, r, "example.com", "443")
		conn.Close()
		return response.StatusCode == http.StatusOK
	})
}

func TestTheLimitOfOneAgentDoesNotStopAnother(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.MaxPerAgent = 1
	r.proxy.SetPolicy(&Policy{Hash: "h", Profiles: map[string]Profile{"p": {Allow: []string{"example.com"}}}})
	r.proxy.BindAgent("coder", Binding{Token: "tok", Profile: "p"})
	r.proxy.BindAgent("reviewer", Binding{Token: "tok2", Profile: "p"})
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
	if name, err := parseServerName(hello); err != nil || name != "example.com" {
		t.Errorf("name = %q, err = %v", name, err)
	}
}

func TestAClientHelloWithEncryptedClientHelloIsRefused(t *testing.T) {
	for _, kind := range []uint16{0xfe0d, 0xff03} {
		hello := buildHello(serverNameExtension("example.com"), extension(kind, []byte{0, 1, 2, 3}))
		if _, err := parseServerName(hello); err == nil || !strings.Contains(err.Error(), "Encrypted Client Hello") {
			t.Errorf("extension %#x: err = %v", kind, err)
		}
	}
}

func TestAnECHClientHelloIsDeniedAtTheTunnel(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	hello := buildHello(serverNameExtension("example.com"), extension(0xfe0d, []byte{0, 1}))
	record := append([]byte{recordTypeHandshake, 3, 1, byte(len(hello) >> 8), byte(len(hello))}, hello...)
	conn.Write(record)
	waitFor(t, func() bool { return strings.Contains(r.audit.String(), "Encrypted Client Hello") })
	if r.requests() != 0 {
		t.Error("the upstream was contacted")
	}
}

func TestParseServerNameHandlesFragmentsAndLongNamesWithoutPanicking(t *testing.T) {
	good := buildHello(serverNameExtension("example.com"))
	for i := 0; i < len(good); i++ {
		parseServerName(good[:i]) // must not panic on any truncation
	}
	if _, err := parseServerName(buildHello(extension(extensionServerName, []byte{0xff, 0xff, 0, 0xff, 0xff}))); err == nil {
		t.Error("a server name list longer than its extension was accepted")
	}
}

// --- R-11: the audit log is bounded and records how much went through ------------------------------------

func TestAuditFieldsFromTheAgentAreTruncated(t *testing.T) {
	var out syncBuffer
	audit := NewAudit(&out)
	audit.Log(Event{Agent: strings.Repeat("a", 5000), Host: strings.Repeat("h", 5000), Action: "deny", Reason: strings.Repeat("r", 5000)})
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
	NewAudit(&out).Log(Event{Action: "deny", Reason: strings.Repeat("é", 400)})
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
	waitFor(t, func() bool { return strings.Contains(r.audit.String(), `"action":"close"`) })
	var closed Event
	for _, line := range strings.Split(r.audit.String(), "\n") {
		if strings.Contains(line, `"action":"close"`) {
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
	if !strings.Contains(text, "egzo up") {
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

func TestAKeyThatDoesNotMatchTheCertificateIsRefused(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	LoadOrCreateCA(a)
	LoadOrCreateCA(b)
	key, _ := os.ReadFile(filepath.Join(b, "ca.key"))
	os.WriteFile(filepath.Join(a, "ca.key"), key, 0o600)
	if _, err := LoadOrCreateCA(a); err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("a mismatched pair was loaded: %v", err)
	}
}

func TestAnUnreadableOrHalfPresentCAIsNeverReplacedSilently(t *testing.T) {
	for _, remove := range []string{"ca.crt", "ca.key"} {
		dir := t.TempDir()
		first, _ := LoadOrCreateCA(dir)
		os.Remove(filepath.Join(dir, remove))
		if again, err := LoadOrCreateCA(dir); err == nil {
			t.Errorf("without %s a new CA (%s) replaced %s", remove, again.Fingerprint()[:8], first.Fingerprint()[:8])
		}
	}
	dir := t.TempDir()
	LoadOrCreateCA(dir)
	os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("truncated"), 0o644)
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Error("a corrupt certificate was replaced silently")
	}
}

func TestTheCAFilesAreWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	LoadOrCreateCA(dir)
	RotateCA(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left %s behind", e.Name())
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "ca.key"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", info.Mode().Perm())
	}
}

func operatorFor(t *testing.T, cfg Config) (http.Handler, *Server, *CA) {
	t.Helper()
	ca, err := LoadOrCreateCA(cfg.CADir)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(ca, NewAudit(io.Discard))
	return operatorHandler(server, ca, cfg), server, ca
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
	if response := call(handler, "POST", "/ca/rotate", ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	onDisk, err := LoadOrCreateCA(cfg.CADir)
	if err != nil || onDisk.Fingerprint() != before {
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
	response := call(handler, "POST", "/ca/rotate", "")
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
		"no hash":      `{"profiles":{}}`,
		"oversized":    `{"hash":"x","profiles":{"a":{"allow":["` + strings.Repeat("a", 9<<20) + `"]}}}`,
		"wrong shapes": `{"hash":"x","profiles":[]}`,
	} {
		if response := call(handler, "PUT", "/policy", body); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, response.Code)
		}
	}
	if server.PolicyHash() != "" {
		t.Error("a rejected policy was loaded")
	}
	if response := call(handler, "PUT", "/policy", `{"hash":"abc","profiles":{}}`); response.Code != http.StatusNoContent {
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
	bind := `{"token":"t","profile":"p"}`
	if response := call(handler, "PUT", "/agents/coder", bind); response.Code != http.StatusConflict {
		t.Errorf("binding before a policy was loaded: status = %d", response.Code)
	}
	if response := call(handler, "PUT", "/policy", `{"hash":"h","profiles":{"p":{"allow":["example.com"]}}}`); response.Code != http.StatusNoContent {
		t.Fatalf("policy: status = %d", response.Code)
	}
	for name, c := range map[string]struct{ path, body string }{
		"a name with a slash": {"/agents/a%2Fb", bind},
		"an upper case name":  {"/agents/Coder", bind},
		"no token":            {"/agents/coder", `{"profile":"p"}`},
		"not json":            {"/agents/coder", `{`},
		"an unknown profile":  {"/agents/coder", `{"token":"t","profile":"nope"}`},
		"an oversized body":   {"/agents/coder", `{"token":"` + strings.Repeat("a", 70<<10) + `","profile":"p"}`},
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
	call(handler, "PUT", "/policy", `{"hash":"h2","profiles":{"p":{"allow":["other.example"]}}}`)
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
