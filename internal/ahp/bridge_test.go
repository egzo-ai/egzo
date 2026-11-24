// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// an echo server that wants the token, and answers each message with itself
func echoServer(t *testing.T, token string) string {
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
		for {
			kind, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			// spread over lines, as a server may: the bridge makes it one
			if err := conn.Write(r.Context(), kind, []byte(strings.Replace(string(data), ",", ",\n", 1))); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestBridgeCarriesLinesAndSaysTheMainSession(t *testing.T) {
	url := echoServer(t, "tok")
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
