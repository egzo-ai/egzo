// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer that is safe to read while the proxy writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rig is a proxy in front of a real TLS upstream, with a client that only trusts the project CA.
type rig struct {
	t        *testing.T
	proxy    *Server
	front    *httptest.Server
	upstream *httptest.Server
	ca       *CA
	audit    *syncBuffer
	mu       sync.Mutex
	seen     []http.Header
	protos   []string
}

func newRig(t *testing.T, upstreamHandler http.HandlerFunc) *rig {
	t.Helper()
	r := &rig{t: t, audit: &syncBuffer{}}
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.ca = ca

	r.upstream = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.seen = append(r.seen, req.Header.Clone())
		r.protos = append(r.protos, req.Proto)
		r.mu.Unlock()
		if upstreamHandler != nil {
			upstreamHandler(w, req)
			return
		}
		io.WriteString(w, "upstream says hello")
	}))
	r.upstream.EnableHTTP2 = true
	r.upstream.StartTLS()
	t.Cleanup(r.upstream.Close)

	roots := x509.NewCertPool()
	roots.AddCert(r.upstream.Certificate())
	dialUpstream := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, r.upstream.Listener.Addr().String())
	}
	r.proxy = NewServer(ca, NewAudit(r.audit))
	r.proxy.Transport = &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext:       dialUpstream,
		TLSClientConfig:   &tls.Config{RootCAs: roots},
	}
	r.proxy.Dial = dialUpstream

	r.front = httptest.NewServer(r.proxy)
	t.Cleanup(r.front.Close)
	return r
}

// setPolicy loads a profile of the agent's own name and binds the agent to it.
func (r *rig) setPolicy(agent, token string, allow []string, services ...Service) {
	r.proxy.SetPolicy(&Policy{Hash: "h", Profiles: map[string]Profile{agent: {Allow: allow, Services: services}}})
	if err := r.proxy.BindAgent(agent, Binding{Instance: testInstance, Token: token, Profile: agent}); err != nil {
		panic(err)
	}
}

// client returns an HTTP client that goes through the proxy as the given agent and trusts trust.
func (r *rig) client(agent, token string, trust *x509.Certificate) *http.Client {
	proxyURL, _ := url.Parse(r.front.URL)
	proxyURL.User = url.UserPassword(agent, token)
	roots := x509.NewCertPool()
	roots.AddCert(trust)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}}
}

func (r *rig) requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

func (r *rig) lastHeader() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		r.t.Fatal("the upstream saw no request")
	}
	return r.seen[len(r.seen)-1]
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var anthropic = Service{Name: "anthropic", Hosts: []string{"example.com"}, Header: "x-api-key", Secret: "REAL-SECRET"}

func TestInjectsTheCredentialAndReplacesWhatTheAgentSent(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)

	request, _ := http.NewRequest("GET", "https://example.com/v1/messages", nil)
	request.Header.Set("x-api-key", "placeholder-from-the-agent")
	response, err := r.client("coder", "tok", r.ca.Cert).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, response); got != "upstream says hello" {
		t.Errorf("body = %q", got)
	}
	if got := r.lastHeader().Get("x-api-key"); got != "REAL-SECRET" {
		t.Errorf("upstream saw x-api-key = %q, want the real secret", got)
	}
	if strings.Contains(r.audit.String(), "REAL-SECRET") {
		t.Error("the audit log contains the secret")
	}
	if !strings.Contains(r.audit.String(), `"mode":"inject"`) {
		t.Errorf("audit should record the interception:\n%s", r.audit.String())
	}
}

func TestTheEventsOfAConnectionShareItsIdAndATerminatedConnectionIsClosedLikeATunnel(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	for range 2 {
		client := r.client("coder", "tok", r.ca.Cert)
		body(t, mustGet(t, client, "https://example.com/a"))
		body(t, mustGet(t, client, "https://example.com/b"))
		client.CloseIdleConnections()
	}
	waitFor(t, func() bool { return strings.Count(r.audit.String(), `"event":"close"`) == 2 })
	byConn := map[string][]string{}
	var order []string
	for _, event := range r.events("example.com") {
		if event.Protocol != HTTPS || event.Agent != "coder" || event.Instance != testInstance || event.Profile != "coder" {
			t.Errorf("event = %+v", event)
		}
		if _, seen := byConn[event.Conn]; !seen {
			order = append(order, event.Conn)
		}
		byConn[event.Conn] = append(byConn[event.Conn], step(event)+" "+event.Path)
	}
	if len(order) != 2 || len(order[0]) != 16 {
		t.Fatalf("connections = %q", order)
	}
	for _, id := range order {
		want := []string{"connect/allow/inject ", "request/allow /a", "request/allow /b", "close "}
		if !slices.Equal(byConn[id], want) {
			t.Errorf("connection %s = %q, want %q", id, byConn[id], want)
		}
	}
}

func mustGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestInjectsWithAValueTemplate(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, Service{Name: "gh", Hosts: []string{"example.com"}, Header: "Authorization", Value: "Bearer {secret}", Secret: "TOK"})
	response, err := r.client("coder", "tok", r.ca.Cert).Get("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	if got := r.lastHeader().Get("Authorization"); got != "Bearer TOK" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestInjectsOnlyUnderTheServicePathsAndRefusesAPathThatCouldClimbOut(t *testing.T) {
	r := newRig(t, nil)
	limited := anthropic
	limited.Paths = []string{"/v1/"}
	r.setPolicy("coder", "tok", nil, limited)
	client := r.client("coder", "tok", r.ca.Cert)

	for path, want := range map[string]string{"/v1/messages": "REAL-SECRET", "/console": "", "/v1x": "", "/v1/files/a%2Fb": "REAL-SECRET"} {
		body(t, mustGet(t, client, "https://example.com"+path))
		if got := r.lastHeader().Get("x-api-key"); got != want {
			t.Errorf("%s: upstream saw x-api-key = %q, want %q", path, got, want)
		}
	}
	seen := r.requests()
	response := mustGet(t, client, "https://example.com/v1/../console")
	body(t, response)
	if response.StatusCode != http.StatusBadRequest || r.requests() != seen {
		t.Errorf("status = %d, upstream requests %d -> %d: a climbing path must be refused", response.StatusCode, seen, r.requests())
	}

	requests := map[string]Event{}
	for _, event := range auditEvents(t, r.audit.String()) {
		if event.Kind == KindConnect && (event.Mode != ModeInject || event.Service != "anthropic") {
			t.Errorf("connect = %+v, want mode inject and service anthropic", event)
		}
		if event.Kind == KindRequest {
			requests[event.Path] = event
		}
	}
	if got := requests["/v1/messages"]; got.Injected != "anthropic" || got.Reason != nil || got.RawPath != "" {
		t.Errorf("injected request = %+v", got)
	}
	if got := requests["/console"]; got.Injected != "" || got.Reason == nil || got.Reason.Code != CodeOutsidePaths || !strings.Contains(got.Reason.Text, `"anthropic"`) {
		t.Errorf("request outside the paths = %+v", got)
	}
	if got := requests["/v1/files/a/b"]; got.Injected != "anthropic" || got.RawPath != "/v1/files/a%2Fb" {
		t.Errorf("request with an escaped slash = %+v", got)
	}
	if got := requests["/v1/../console"]; got.Decision != OutcomeDeny || got.Injected != "" || got.Reason == nil || got.Reason.Code != CodePath {
		t.Errorf("refused request = %+v", got)
	}
}

// auditEvents reads the audit log, each event as the proxy writes it.
func auditEvents(t *testing.T, log string) []Event {
	t.Helper()
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		if err := event.Check(); err != nil {
			t.Errorf("%v: %s", err, line)
		}
		events = append(events, event)
	}
	return events
}

func TestAnInjectionCoversWholeSegmentsAndRefusesAmbiguousPathsUnderItsPrefixes(t *testing.T) {
	inject := Injection{Paths: []string{"/api/", "/upload"}}
	cases := []struct {
		escaped string
		covered bool
		refuse  string // a part of the reason, or "" when not refused
	}{
		{"/api/v1", true, ""},
		{"/api/", true, ""},
		{"/api", false, ""},
		{"/upload", true, ""},
		{"/upload/x", true, ""},
		{"/uploads", false, ""},
		{"/console", false, ""},
		{"/other/../api/x", false, ""},
		{"/console/%2e%2e/x", false, ""}, // nothing is injected there, whatever the server makes of it

		// what APIs send
		{"/api/projects/group%2Fproject", true, ""},
		{"/api/contents/my%20file.md", true, ""},
		{"/api/v1/models/gemini:generateContent", true, ""},
		{"/api/v1/users/a@b.example;v=2", true, ""},
		{"/api/v1.2/x", true, ""},
		{"/api/x..y", true, ""},

		// what they never send
		{"/api/../console", false, "segment"},
		{"/api/./v1", false, "segment"},
		{"/api/...", false, "segment"},
		{"/api/..;/console", false, "segment"},
		{"/api/..%20/console", false, "segment"},
		{"/api/%2e%2e/console", false, "needs no encoding"},
		{"/api/%2E%2E%2Fconsole", false, "needs no encoding"},
		{"/%61pi/x", false, "needs no encoding"},
		{"/api/%252e%252e/console", false, "twice"},
		{"/api/..%5cconsole", false, "backslash"},
		{"/api/..\\console", false, "does not carry"},
		{"/api/..%00/console", false, "control"},
		{"/api/%EF%BC%8E%EF%BC%8E/console", false, "outside ASCII"},
		{"/api/x%", false, "not an escape"},
		{"/api/x%zz", false, "not an escape"},
		{"/api/{x}", false, "does not carry"},
	}
	for _, c := range cases {
		covered, refuse := inject.Covers(c.escaped)
		if covered != c.covered || (c.refuse == "") != (refuse == "") || !strings.Contains(refuse, c.refuse) {
			t.Errorf("%s: covered %v refuse %q, want %v %q", c.escaped, covered, refuse, c.covered, c.refuse)
		}
	}
	if covered, refuse := (&Injection{}).Covers("/x/../y"); !covered || refuse != "" {
		t.Error("an injection without paths covers every path")
	}
}

func TestSpeaksHTTP1ToTheAgentWhateverTheUpstreamSpeaks(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	response, err := r.client("coder", "tok", r.ca.Cert).Get("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	if response.ProtoMajor != 1 {
		t.Errorf("the agent got %s: clients such as Bun cannot parse an HTTP/2 status line", response.Proto)
	}
	r.mu.Lock()
	proto := r.protos[0]
	r.mu.Unlock()
	if proto != "HTTP/2.0" {
		t.Errorf("the upstream was reached with %s, want HTTP/2.0", proto)
	}
}

func TestBodilessResponsesPassThroughIntact(t *testing.T) {
	for _, status := range []int{http.StatusNotModified, http.StatusNoContent} {
		r := newRig(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		r.setPolicy("coder", "tok", nil, anthropic)
		client := r.client("coder", "tok", r.ca.Cert)
		for i := 0; i < 3; i++ { // reuse the kept-alive connection: a malformed response would corrupt it
			response, err := client.Get("https://example.com/")
			if err != nil {
				t.Fatalf("status %d request %d: %v", status, i, err)
			}
			if response.StatusCode != status {
				t.Errorf("status = %d, want %d", response.StatusCode, status)
			}
			body(t, response)
		}
	}
}

func TestStreamsResponsesAsTheyArrive(t *testing.T) {
	release := make(chan struct{})
	r := newRig(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "first chunk\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "second chunk\n")
	})
	r.setPolicy("coder", "tok", nil, anthropic)
	response, err := r.client("coder", "tok", r.ca.Cert).Get("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "first chunk\n" {
		t.Fatalf("first chunk = %q, %v (the proxy buffered the stream)", line, err)
	}
	close(release)
}

func TestAllowedHostsWithoutInjectionAreTunnelledNotIntercepted(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	// The client trusts only the upstream's own certificate: if the proxy intercepted, this would fail.
	response, err := r.client("coder", "tok", r.upstream.Certificate()).Get("https://example.com/")
	if err != nil {
		t.Fatalf("a tunnel must be transparent: %v", err)
	}
	body(t, response)
	if got := r.lastHeader().Get("x-api-key"); got != "" {
		t.Errorf("a credential was injected into a tunnel: %q", got)
	}
	if !strings.Contains(r.audit.String(), `"decision":"allow","mode":"tunnel"`) {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

func TestDeniesHostsOutsideTheProfile(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	_, err := r.client("coder", "tok", r.ca.Cert).Get("https://evil.example/")
	if err == nil {
		t.Fatal("a host outside the profile was reachable")
	}
	if !strings.Contains(r.audit.String(), `"decision":"deny"`) || !strings.Contains(r.audit.String(), `"code":"not-in-profile"`) || !strings.Contains(r.audit.String(), "evil.example") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
	if r.requests() != 0 {
		t.Error("the upstream was contacted")
	}
}

func TestEveryEventOfAnAgentCarriesTheIdOfItsInstanceAndABindingNeedsOne(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	r.client("coder", "tok", r.ca.Cert).Get("https://evil.example/")
	if !strings.Contains(r.audit.String(), `"agent":"coder","instance":"`+testInstance+`"`) {
		t.Errorf("audit:\n%s", r.audit.String())
	}
	for _, id := range []string{"", "issue-1", "01jbx7q0000000000000000000", "01JBX7Q000000000000000000I"} {
		if err := r.proxy.BindAgent("reviewer", Binding{Instance: id, Token: "t2", Profile: "coder"}); err == nil {
			t.Errorf("a binding with the instance id %q was accepted", id)
		}
	}
}

func TestEverythingIsDeniedUntilAPolicyIsLoaded(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.client("coder", "tok", r.ca.Cert).Get("https://example.com/"); err == nil {
		t.Fatal("reachable without a policy")
	}
}

func TestRequiresValidProxyCredentials(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"*"})

	connect := func(authorization string) int {
		conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n"
		if authorization != "" {
			request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(authorization)) + "\r\n"
		}
		io.WriteString(conn, request+"\r\n")
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode
	}
	for name, authorization := range map[string]string{"none": "", "wrong token": "coder:nope", "unknown agent": "ghost:tok"} {
		if got := connect(authorization); got != http.StatusProxyAuthRequired {
			t.Errorf("%s: status = %d, want 407", name, got)
		}
	}
	if got := connect("coder:tok"); got != http.StatusOK {
		t.Errorf("valid credentials: status = %d, want 200", got)
	}
}

func TestNoCredentialsIsAChallengeAndWrongCredentialsAreADenial(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"*"})
	connect := func(header string) *http.Response {
		conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n"+header+"\r\n")
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	basic := func(login string) string {
		return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(login)) + "\r\n"
	}
	response := connect("")
	if response.StatusCode != http.StatusProxyAuthRequired || response.Header.Get("Proxy-Authenticate") == "" {
		t.Fatalf("no credentials: %d, challenge %q", response.StatusCode, response.Header.Get("Proxy-Authenticate"))
	}
	if events := r.events("example.com"); len(events) != 1 || events[0].Decision != OutcomeChallenge || events[0].Agent != "" || events[0].Reason.Code != CodeNoCredentials {
		t.Fatalf("events after a bare CONNECT = %+v", events)
	}

	headers := []string{basic("coder:nope"), basic("ghost:tok"), "Proxy-Authorization: Bearer abc\r\n", "Proxy-Authorization: Basic !!!\r\n", "Proxy-Authorization: \r\n"}
	for _, header := range headers {
		if got := connect(header).StatusCode; got != http.StatusProxyAuthRequired {
			t.Errorf("%q: status = %d", header, got)
		}
	}
	events := r.events("example.com")[1:]
	if len(events) != len(headers) {
		t.Fatalf("events = %+v", events)
	}
	for _, event := range events {
		if event.Decision != OutcomeDeny || event.Reason.Code != CodeCredentials || event.Instance != "" || event.Profile != "" {
			t.Errorf("event = %+v", event)
		}
	}
	// the name is only claimed: it is never the agent
	for i, want := range []string{"coder", "ghost", "", "", ""} {
		if events[i].Agent != "" || events[i].ClaimedAgent != want {
			t.Errorf("event %d: agent %q, claimed %q; want claimed %q", i, events[i].Agent, events[i].ClaimedAgent, want)
		}
	}
	for _, event := range r.events("example.com") {
		if event.Client != "127.0.0.1" {
			t.Errorf("client = %q", event.Client)
		}
	}
}

func TestAnAuthenticatedConnectionSaysWhoByTheCredentialsWhatWasClaimedWhereFromAndTheUserAgent(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"*"})
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT 10.0.0.1:443 HTTP/1.1\r\nHost: 10.0.0.1:443\r\nUser-Agent: curl/8.11.1\r\n"+
		"Proxy-Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("coder:tok"))+"\r\n\r\n")
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
		t.Fatal(err)
	}
	events := r.events("10.0.0.1")
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	e := events[0]
	if e.Agent != "coder" || e.ClaimedAgent != "coder" || e.Instance != testInstance || e.Client != "127.0.0.1" || e.UserAgent != "curl/8.11.1" {
		t.Errorf("event = %+v", e)
	}
}

func TestOnlyPort443IsReachable(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"*"})
	conn, _ := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	io.WriteString(conn, "CONNECT example.com:22 HTTP/1.1\r\nHost: example.com:22\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.StatusCode)
	}
}

func TestRefusesPlainHTTPRequests(t *testing.T) {
	r := newRig(t, nil)
	response, err := http.Get(r.front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", response.StatusCode)
	}
}

// tunnelTo opens a CONNECT tunnel and returns the raw connection after the proxy's 200.
func (r *rig) tunnelTo(host string) net.Conn {
	r.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { conn.Close() })
	auth := base64.StdEncoding.EncodeToString([]byte("coder:tok"))
	io.WriteString(conn, "CONNECT "+host+":443 HTTP/1.1\r\nHost: "+host+":443\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || response.StatusCode != http.StatusOK {
		r.t.Fatalf("CONNECT: %v %v", response, err)
	}
	return conn
}

func TestRefusesATLSServerNameThatDiffersFromTheTunnelHost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	conn := r.tunnelTo("example.com")
	handshake := tls.Client(conn, &tls.Config{ServerName: "other.example", InsecureSkipVerify: true})
	if err := handshake.Handshake(); err == nil {
		t.Fatal("the proxy minted a certificate for a host other than the approved one")
	}
}

func TestRefusesRequestsInsideATunnelForAnotherHost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	conn := r.tunnelTo("example.com")
	roots := x509.NewCertPool()
	roots.AddCert(r.ca.Cert)
	secure := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: roots})
	if err := secure.Handshake(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(secure, "GET / HTTP/1.1\r\nHost: attacker.example\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(secure), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want 421", response.StatusCode)
	}
	if r.requests() != 0 {
		t.Error("the upstream was contacted for a different host")
	}
}

func TestADroppedHandshakeIsLoggedAsAbortedNotDenied(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", nil, anthropic)
	conn := r.tunnelTo("example.com")
	conn.Close() // a pre-warmed connection the client never used
	for i := 0; i < 100 && !strings.Contains(r.audit.String(), "aborted"); i++ {
		waitABit()
	}
	if !strings.Contains(r.audit.String(), `"code":"aborted"`) || strings.Contains(r.audit.String(), `"decision":"deny"`) {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

func TestRefusesATunnelWhoseTLSServerNameDiffersFromTheApprovedHost(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"}) // tunnelled, not intercepted
	conn := r.tunnelTo("example.com")
	handshake := tls.Client(conn, &tls.Config{ServerName: "other.example", InsecureSkipVerify: true})
	if err := handshake.Handshake(); err == nil {
		t.Fatal("a tunnel approved for example.com carried TLS for another name")
	}
	for i := 0; i < 100 && !strings.Contains(r.audit.String(), "differs"); i++ {
		waitABit()
	}
	if !strings.Contains(r.audit.String(), `"code":"sni-mismatch"`) || !strings.Contains(r.audit.String(), "other.example") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
	if r.requests() != 0 {
		t.Error("the upstream was contacted")
	}
}

func TestRefusesATunnelWithoutAServerName(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	// An address has no SNI: no way to know which server the client really wants.
	handshake := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: ""})
	if err := handshake.Handshake(); err == nil {
		t.Fatal("a ClientHello without a server name was tunnelled")
	}
}

func TestRefusesATunnelThatIsNotTLS(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	conn := r.tunnelTo("example.com")
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	for i := 0; i < 100 && !strings.Contains(r.audit.String(), "deny"); i++ {
		waitABit()
	}
	if !strings.Contains(r.audit.String(), "ClientHello") {
		t.Errorf("audit:\n%s", r.audit.String())
	}
	if r.requests() != 0 {
		t.Error("plain HTTP reached the upstream through a tunnel")
	}
}

func TestParseServerNameHandlesOddHellos(t *testing.T) {
	for name, hello := range map[string][]byte{
		"empty":     {},
		"wrong":     {0x02, 0, 0, 1, 0},
		"truncated": {0x01, 0, 0, 40, 3, 3},
	} {
		if got, _, err := parseServerName(hello); err == nil && got != "" {
			t.Errorf("%s: parsed %q from garbage", name, got)
		}
	}
}

// testInstance is the id of every instance a test binds.
const testInstance = "01JBX7Q0000000000000000000"

// first is the first of two results.
func first[T, U any](value T, _ U) T { return value }
