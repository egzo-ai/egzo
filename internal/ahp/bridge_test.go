// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// an AHP server that wants the token: it answers a connection only once it initialized, as the plugin does; then listSessions with the
// sessions it knows, and every other message with itself
type fakeServer struct {
	mu    sync.Mutex
	known []string // ids of the sessions it lists
	lists int      // listSessions it was asked
}

func (f *fakeServer) know(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known = append(f.known, id)
}

func echoServer(t *testing.T, token string) string {
	url, _ := sessionServer(t, token)
	return url
}

func sessionServer(t *testing.T, token string) (string, *fakeServer) {
	f := &fakeServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		initialized := false
		for {
			kind, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(data, &m)
			if m.Method == "initialize" || m.Method == "reconnect" {
				initialized = true
				answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": "1.0.0"}})
				if err := conn.Write(r.Context(), kind, answer); err != nil {
					return
				}
				continue
			}
			if m.Method == "listSessions" && !initialized {
				answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32600, "message": "not initialized"}})
				if err := conn.Write(r.Context(), kind, answer); err != nil {
					return
				}
				continue
			}
			if m.Method == "listSessions" {
				f.mu.Lock()
				f.lists++
				items := []map[string]string{}
				for _, id := range f.known {
					items = append(items, map[string]string{"resource": "ahp-session:/" + id})
				}
				f.mu.Unlock()
				answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"items": items}})
				if err := conn.Write(r.Context(), kind, answer); err != nil {
					return
				}
				continue
			}
			// spread over lines, as a server may: the bridge makes it one
			if err := conn.Write(r.Context(), kind, []byte(strings.Replace(string(data), ",", ",\n", 1))); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), f
}

func TestBridgeCarriesLinesAndSaysTheMainSession(t *testing.T) {
	url, server := sessionServer(t, "tok")
	server.know("ses_main")
	mainFile := filepath.Join(t.TempDir(), "main-session")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, url, "tok", mainFile, inR, outW) }()
	lines := bufio.NewScanner(outR)

	go inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
	if !lines.Scan() || lines.Text() != `{"jsonrpc":"2.0","id":1,"method":"ping"}` {
		t.Fatalf("echo: %q", lines.Text())
	}
	if err := os.WriteFile(mainFile, []byte("ses_main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !lines.Scan() || !strings.Contains(lines.Text(), `"method":"x-egzo/mainSession"`) || !strings.Contains(lines.Text(), `"chat":"ahp-chat:/ses_main"`) {
		t.Fatalf("main session note: %q", lines.Text())
	}
	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the bridge does not end with its input")
	}
}

func TestBridgeRefusedWithoutTheToken(t *testing.T) {
	url := echoServer(t, "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := Bridge(ctx, url, "wrong", "", strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("a wrong token is refused")
	}
}

// egzo names the main session before OpenCode makes it, and the AHP server learns of a session it did not make late, or when it is
// listed: the bridge says which session is the main one only once the server lists it, so a client can subscribe to it as soon as it
// hears. It asks on a connection of its own: the client, which has not initialized (a page that reconnects waits for the main session
// before it does), sees none of it.
func TestBridgeSaysTheMainSessionOnceTheServerKnowsIt(t *testing.T) {
	url, server := sessionServer(t, "tok")
	mainFile := filepath.Join(t.TempDir(), "main-session")
	if err := os.WriteFile(mainFile, []byte("ses_main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	defer inW.Close()
	out := &lockedLines{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go Bridge(ctx, url, "tok", mainFile, inR, out)
	time.Sleep(2500 * time.Millisecond)
	if got := out.text(); strings.Contains(got, MainSessionMethod) {
		t.Fatalf("said the main session before the server knew it: %s", got)
	}
	server.mu.Lock()
	asked := server.lists
	server.mu.Unlock()
	if asked == 0 {
		t.Fatal("the bridge never asked the server")
	}
	server.know("ses_main")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.text(), MainSessionMethod) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	got := out.text()
	if !strings.Contains(got, `"chat":"ahp-chat:/ses_main"`) {
		t.Fatalf("the main session was not said once the server knew it: %s", got)
	}
	if strings.Contains(got, "items") || strings.Contains(got, "protocolVersion") || strings.Contains(got, "not initialized") {
		t.Errorf("what the bridge asked for its own sake reached the client: %s", got)
	}
}

type lockedLines struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
