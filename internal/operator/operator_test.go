package operator

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// serveOnSocket exposes handler on a unix socket the way a sidecar does, without Serve's
// signal handling, and returns the socket path.
func serveOnSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	// Unix socket paths are limited to ~100 bytes, which t.TempDir can exceed.
	dir, err := os.MkdirTemp("", "op")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket
}

func TestRequest(t *testing.T) {
	socket := serveOnSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte(r.Method + " " + r.URL.RequestURI() + " " + string(body)))
	}))

	got, err := Request(socket, http.MethodPut, "/specs/abc?x=1", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "PUT /specs/abc?x=1 payload"; string(got) != want {
		t.Errorf("response = %q, want %q", got, want)
	}
}

func TestRequestTurnsNon2xxIntoAnErrorCarryingTheBody(t *testing.T) {
	socket := serveOnSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such spec\n", http.StatusNotFound)
	}))

	body, err := Request(socket, http.MethodGet, "/specs/x", nil)
	if err == nil {
		t.Fatal("a 404 was not an error")
	}
	for _, fragment := range []string{"GET /specs/x", "404", "no such spec"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q lacks %q", err, fragment)
		}
	}
	if !strings.Contains(string(body), "no such spec") {
		t.Errorf("body = %q, want it returned alongside the error", body)
	}
}

func TestRequestFailsWhenNothingListens(t *testing.T) {
	if _, err := Request(filepath.Join(t.TempDir(), "absent.sock"), http.MethodGet, "/", nil); err == nil {
		t.Error("expected a dial error")
	}
}

func TestHealthcheck(t *testing.T) {
	healthy := true
	socket := serveOnSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || !healthy {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		}
	}))
	if err := Healthcheck(socket); err != nil {
		t.Errorf("healthy sidecar: %v", err)
	}
	healthy = false
	if err := Healthcheck(socket); err == nil {
		t.Error("an unhealthy sidecar passed the healthcheck")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func TestStreamCopiesTheBodyUntilItEnds(t *testing.T) {
	socket := serveOnSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, line := range []string{"one\n", "two\n", "three\n"} {
			w.Write([]byte(line))
			w.(http.Flusher).Flush()
		}
	}))
	var out syncBuffer
	if err := Stream(socket, http.MethodGet, "/events", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.buf.String(); got != "one\ntwo\nthree\n" {
		t.Errorf("streamed %q", got)
	}
}

func TestStreamReportsNon2xx(t *testing.T) {
	socket := serveOnSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	var out syncBuffer
	err := Stream(socket, http.MethodGet, "/events", &out)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("err = %v", err)
	}
	if out.buf.Len() != 0 {
		t.Errorf("an error body leaked to the output: %q", out.buf.String())
	}
}

func TestServeAnswersOnTheSocketAndReplacesAStaleOne(t *testing.T) {
	dir, err := os.MkdirTemp("", "serve")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "nested", "s.sock")

	// A socket file left over from a crashed run must not stop the next start.
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	go Serve(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	var got []byte
	for range 200 {
		if got, err = Request(socket, http.MethodGet, "/", nil); err == nil {
			break
		}
		waitABit()
	}
	if err != nil || string(got) != "ok" {
		t.Fatalf("Request = %q, %v", got, err)
	}
}
