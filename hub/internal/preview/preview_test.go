// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package preview

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	domain = "egzo-preview.example.net"
	id     = "abcdefghijklmnop"
	host   = id + "-p1a2b3c4d5e6." + domain
)

// agent is one server behind a Backend, on a real socket: the proxy sees what a link gives it, a net.Conn.
type agent struct {
	t      *testing.T
	server *httptest.Server
	find   error
	dials  atomic.Int32
	open   atomic.Int32
	dial   func(ctx context.Context) (net.Conn, error)
}

func (a *agent) Find(_ context.Context, project, wanted string) (Target, error) {
	if a.find != nil {
		return Target{}, a.find
	}
	if project != "p1a2b3c4d5e6" || wanted != id {
		return Target{}, ErrNotFound
	}
	return Target{Project: project, ID: wanted, Agent: "coder-1", Port: 3000}, nil
}

type counted struct {
	net.Conn
	once sync.Once
	gone func()
}

func (c *counted) Close() error { c.once.Do(c.gone); return c.Conn.Close() }

func (a *agent) Dial(ctx context.Context, _ Target) (net.Conn, error) {
	a.dials.Add(1)
	if a.dial != nil {
		return a.dial(ctx)
	}
	conn, err := net.Dial("tcp", a.server.Listener.Addr().String())
	if err != nil {
		return nil, err
	}
	a.open.Add(1)
	return &counted{Conn: conn, gone: func() { a.open.Add(-1) }}, nil
}

type gate struct {
	refuse  bool
	entered atomic.Int32
}

func (g *gate) Enter(w http.ResponseWriter, _ *http.Request, _ Host) { g.entered.Add(1) }
func (g *gate) Allow(w http.ResponseWriter, r *http.Request, _ Host) (context.Context, bool) {
	if g.refuse {
		http.Redirect(w, r, "https://hub.example.com/preview", http.StatusFound)
		return nil, false
	}
	return r.Context(), true
}
func (g *gate) StripRequest(header http.Header)  { header.Del("X-Gate") }
func (g *gate) StripResponse(header http.Header) { header.Del("X-Gate") }

func start(t *testing.T, app http.Handler, opts Options, tune ...func(*agent, *gate)) (*httptest.Server, *agent, *gate) {
	t.Helper()
	a, g := &agent{t: t}, &gate{}
	a.server = httptest.NewServer(app)
	t.Cleanup(a.server.Close)
	for _, f := range tune {
		f(a, g)
	}
	opts.Domain, opts.Scheme = domain, "https"
	opts.HostOf = func(r *http.Request) string { return r.Host }
	front := httptest.NewServer(New(a, g, opts, nil))
	t.Cleanup(front.Close)
	return front, a, g
}

func get(t *testing.T, front *httptest.Server, name, path string, headers ...string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, front.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = name
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func text(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHostNames(t *testing.T) {
	for name, want := range map[string]bool{
		host:                           true,
		strings.ToUpper(host):          true,
		id + "-p1a2b3c4d5e6.other.net": false,
		domain:                         false,
		"a.b." + host:                  false,
		"hub.example.com":              false,
		id + "." + domain:              false,
		"x" + host:                     false,
	} {
		if _, ok := ParseHost(name, domain); ok != want {
			t.Errorf("%s: %v", name, ok)
		}
	}
	if got := Template("https", "p1a2b3c4d5e6", domain); got != "https://%s-p1a2b3c4d5e6."+domain+"/" {
		t.Error(got)
	}
}

func TestTheAppSeesItsOwnHostAndTheRealOneInHeaders(t *testing.T) {
	var seen *http.Request
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r; io.WriteString(w, "hello") }), Options{})
	response := get(t, front, host, "/page?x=1", "Cookie", "theirs=1", "X-Gate", "secret")
	if got := text(t, response); got != "hello" {
		t.Fatalf("%d %s", response.StatusCode, got)
	}
	if seen.Host != "localhost:3000" || seen.Header.Get("X-Forwarded-Host") != host || seen.Header.Get("X-Forwarded-Proto") != "https" || seen.URL.RequestURI() != "/page?x=1" {
		t.Errorf("%+v", seen)
	}
	if seen.Header.Get("Cookie") != "theirs=1" || seen.Header.Get("X-Gate") != "" {
		t.Errorf("%+v", seen.Header)
	}
}

func TestAnAnswerComesBackAsTheAppGaveItButForTheGatesTraces(t *testing.T) {
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Gate", "planted")
		http.Redirect(w, r, "http://localhost:3000/login", http.StatusFound)
	}), Options{})
	response := get(t, front, host, "/")
	if got := response.Header.Get("Location"); got != "http://localhost:3000/login" {
		t.Error(got)
	}
	if response.Header.Get("X-Gate") != "" {
		t.Error("the app answered in the gate's name")
	}
}

func TestWhatIsNotAPreviewNeverReachesTheApp(t *testing.T) {
	front, a, g := start(t, http.NotFoundHandler(), Options{})
	if got := get(t, front, "hub.example.com", "/").StatusCode; got != http.StatusNotFound {
		t.Error(got)
	}
	if got := get(t, front, "qqqqqqqqqqqqqqqq-p1a2b3c4d5e6."+domain, "/").StatusCode; got != http.StatusNotFound {
		t.Error(got)
	}
	get(t, front, host, "/_egzo/enter")
	if g.entered.Load() != 1 || a.dials.Load() != 0 {
		t.Errorf("entered %d, dials %d", g.entered.Load(), a.dials.Load())
	}
}

func TestTheGateDecidesFirst(t *testing.T) {
	front, a, _ := start(t, http.NotFoundHandler(), Options{}, func(_ *agent, g *gate) { g.refuse = true })
	if got := get(t, front, host, "/").StatusCode; got != http.StatusFound {
		t.Error(got)
	}
	if a.dials.Load() != 0 {
		t.Error("a request that was not allowed reached the agent")
	}
}

func TestAPageOfAnotherOriginCannotUpgradeOrPost(t *testing.T) {
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }), Options{})
	if got := get(t, front, host, "/", "Upgrade", "websocket", "Connection", "Upgrade", "Origin", "https://other-p1."+domain).StatusCode; got != http.StatusForbidden {
		t.Errorf("an upgrade: %d", got)
	}
	if got := get(t, front, host, "/", "Origin", "https://other-p1."+domain).StatusCode; got != http.StatusOK {
		t.Errorf("a plain GET is the app's: %d", got)
	}
	post := func(origin string) int {
		request, _ := http.NewRequest(http.MethodPost, front.URL+"/", strings.NewReader("x"))
		request.Host = host
		request.Header.Set("Origin", origin)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if got := post("https://evil.example.org"); got != http.StatusForbidden {
		t.Errorf("a post from elsewhere: %d", got)
	}
	if got := post("https://" + host); got != http.StatusOK {
		t.Errorf("its own post: %d", got)
	}
}

func TestAWebSocketPasses(t *testing.T) {
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		kind, data, err := conn.Read(r.Context())
		if err == nil {
			conn.Write(r.Context(), kind, append([]byte("echo "), data...))
		}
	}), Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(front.URL, "http", "ws", 1), &websocket.DialOptions{
		Host: host, HTTPHeader: http.Header{"Origin": {"https://" + host}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn.Read(ctx); err != nil || string(data) != "echo hi" {
		t.Fatalf("%q %v", data, err)
	}
}

func TestAnEventStreamComesOutAsItComes(t *testing.T) {
	release := make(chan struct{})
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
	}), Options{})
	defer close(release)
	response := get(t, front, host, "/")
	buffer := make([]byte, 64)
	read := make(chan string, 1)
	go func() { n, _ := response.Body.Read(buffer); read <- string(buffer[:n]) }()
	select {
	case got := <-read:
		if !strings.Contains(got, "first") {
			t.Error(got)
		}
	case <-time.After(5 * time.Second):
		t.Error("it waited for the end of the answer")
	}
}

func TestAFailureSaysWhatIsWrongInItsOwnWords(t *testing.T) {
	for reason, want := range map[string]struct {
		err    error
		status int
		says   string
	}{
		"nothing": {errors.New("nothing listens on port 3000"), http.StatusBadGateway, "Nothing listens on port 3000 in coder-1 now."},
		"stopped": {errors.New(`agent "coder-1" is not running`), http.StatusBadGateway, "coder-1 is not running."},
		"offline": {ErrOffline, http.StatusServiceUnavailable, "offline"},
		"other":   {errors.New("<script>a link says this</script>"), http.StatusBadGateway, "plain HTTP"},
	} {
		t.Run(reason, func(t *testing.T) {
			front, _, _ := start(t, http.NotFoundHandler(), Options{}, func(a *agent, _ *gate) {
				a.dial = func(context.Context) (net.Conn, error) { return nil, want.err }
			})
			response := get(t, front, host, "/")
			got := text(t, response)
			if response.StatusCode != want.status || !strings.Contains(got, want.says) || strings.Contains(got, "<script>") {
				t.Errorf("%d %s", response.StatusCode, got)
			}
		})
	}
	front, _, _ := start(t, http.NotFoundHandler(), Options{}, func(a *agent, _ *gate) { a.find = ErrOffline })
	if got := get(t, front, host, "/").StatusCode; got != http.StatusServiceUnavailable {
		t.Error(got)
	}
}

func TestAServerThatDoesNotSpeakPlainHTTPIsSaidToBe(t *testing.T) {
	front, _, _ := start(t, http.NotFoundHandler(), Options{}, func(a *agent, _ *gate) {
		a.dial = func(context.Context) (net.Conn, error) {
			near, far := net.Pipe()
			go func() { far.Write([]byte("\x16\x03\x01 not http\r\n\r\n")); far.Close() }()
			return near, nil
		}
	})
	response := get(t, front, host, "/")
	if got := text(t, response); response.StatusCode != http.StatusBadGateway || !strings.Contains(got, "plain HTTP") {
		t.Errorf("%d %s", response.StatusCode, got)
	}
}

func TestASilentServerRunsOutOfTime(t *testing.T) {
	stop := make(chan struct{})
	front, _, _ := start(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stop }), Options{HeaderTime: 50 * time.Millisecond})
	defer close(stop)
	if got := get(t, front, host, "/").StatusCode; got != http.StatusGatewayTimeout {
		t.Error(got)
	}
}

func TestOverTheLimitARequestWaitsThenSaysWhich(t *testing.T) {
	for reason, want := range map[string]struct {
		opts Options
		says string
	}{
		"preview": {Options{PerPreview: 1, PerHub: 10}, "this preview: at most 1."},
		"hub":     {Options{PerPreview: 10, PerHub: 1}, "this hub: at most 1."},
	} {
		t.Run(reason, func(t *testing.T) { overTheLimit(t, want.opts, want.says) })
	}
}

func overTheLimit(t *testing.T, opts Options, says string) {
	opts.Queue = 100 * time.Millisecond
	release := make(chan struct{})
	front, _, _ := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-release
		}
		io.WriteString(w, "ok")
	}), opts)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequest(http.MethodGet, front.URL+"/slow", nil)
		request.Host = host
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	response := get(t, front, host, "/quick")
	if got := text(t, response); response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(got, says) {
		t.Errorf("%d %s", response.StatusCode, got)
	}
	close(release)
	<-done
}

func TestAWaitingRequestGetsTheSlotThatIsFreed(t *testing.T) {
	limit := newLimiter(1, 10)
	release, err := limit.take(context.Background(), id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	took := make(chan error, 1)
	go func() {
		_, err := limit.take(context.Background(), id, 5*time.Second)
		took <- err
	}()
	time.Sleep(20 * time.Millisecond)
	release()
	release() // a connection closes more than once
	if err := <-took; err != nil {
		t.Error(err)
	}
	if limit.all != 1 || limit.open[id] != 1 {
		t.Errorf("%d %d", limit.all, limit.open[id])
	}
	if _, err := limit.take(context.Background(), "another", time.Second); err != nil {
		t.Error(err)
	}
}

// everywhere finds any id in any project, as a link that claims one can make it.
type everywhere struct{ *agent }

func (e everywhere) Find(_ context.Context, project, wanted string) (Target, error) {
	return Target{Project: project, ID: wanted, Agent: "coder-1", Port: 3000}, nil
}

func TestAProjectCannotFillAnotherProjectsPreviewWithTheSameID(t *testing.T) {
	release := make(chan struct{})
	waiting := make(chan struct{})
	a := &agent{t: t}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(waiting)
			<-release
		}
		io.WriteString(w, "ok")
	}))
	t.Cleanup(a.server.Close)
	front := httptest.NewServer(New(everywhere{a}, &gate{}, Options{Domain: domain, Scheme: "https", PerPreview: 1, Queue: 100 * time.Millisecond}, nil))
	t.Cleanup(front.Close)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequest(http.MethodGet, front.URL+"/slow", nil)
		request.Host = id + "-pattacker." + domain
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	<-waiting
	if got := get(t, front, host, "/").StatusCode; got != http.StatusOK {
		t.Error(got)
	}
	close(release)
	<-done
}

func TestOnePreviewDoesNotUseTheConnectionsOfAnother(t *testing.T) {
	if poolKey(Target{ID: "a", Project: "p1", Port: 80}) == poolKey(Target{ID: "b", Project: "p1", Port: 80}) {
		t.Error("the transport would mix them")
	}
}

func TestAnUploadTakesAsLongAsItTakes(t *testing.T) {
	if listener := Listener(":0", http.NotFoundHandler()); listener.ReadTimeout != 0 || listener.ReadHeaderTimeout == 0 {
		t.Errorf("%+v", listener)
	}
}
