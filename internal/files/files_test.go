// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package files

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// tree is a workspace as an agent might leave it, next to a secret it must not reach.
func tree(t *testing.T) (*os.Root, string) {
	t.Helper()
	top := t.TempDir()
	dir := filepath.Join(top, "w")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "notes", "deep"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "notes", "a.txt"), []byte("alpha\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "page.html"), []byte("<script>alert(1)</script>"), 0o644))
	must(os.WriteFile(filepath.Join(top, "secret"), []byte("outside\n"), 0o644))
	must(os.Symlink("../secret", filepath.Join(dir, "out")))
	must(os.Symlink("notes/a.txt", filepath.Join(dir, "in")))
	must(os.Symlink("..", filepath.Join(dir, "notes", "up")))
	must(syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644))
	root, err := os.OpenRoot(dir)
	must(err)
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func get(t *testing.T, h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s %s did not answer: something blocked", method, target)
	}
	return w
}

func listing(t *testing.T, w *httptest.ResponseRecorder) Listing {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var l Listing
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestTheRootListsFilesDirectoriesAndLinksSortedAndNothingElse(t *testing.T) {
	root, _ := tree(t)
	l := listing(t, get(t, Handler{Root: root}, "GET", "/"))
	var got []string
	for _, e := range l.Entries {
		got = append(got, e.Name+":"+e.Type)
		if e.Modified.IsZero() {
			t.Errorf("%s has no modification time", e.Name)
		}
	}
	if want := "in:symlink notes:dir out:symlink page.html:file"; strings.Join(got, " ") != want {
		t.Errorf("entries = %v, want %s (the pipe is not listed)", got, want)
	}
	if l.Truncated {
		t.Error("truncated")
	}
	if l.Entries[3].Size != int64(len("<script>alert(1)</script>")) {
		t.Errorf("size = %d", l.Entries[3].Size)
	}
}

func TestAFileIsItsBytesNeverTypedAndARangeIsServed(t *testing.T) {
	root, _ := tree(t)
	h := Handler{Root: root}
	w := get(t, h, "GET", "/page.html")
	if w.Code != http.StatusOK || w.Body.String() != "<script>alert(1)</script>" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q: what a file holds is not guessed", ct)
	}
	if w := get(t, h, "GET", "/notes/a.txt", "Range", "bytes=1-3"); w.Code != http.StatusPartialContent || w.Body.String() != "lph" {
		t.Errorf("range: %d %q", w.Code, w.Body)
	}
	if w := get(t, h, "HEAD", "/notes/a.txt"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", w.Code, w.Body)
	}
}

func TestTheSlashSaysWhatIsAsked(t *testing.T) {
	root, _ := tree(t)
	h := Handler{Root: root}
	if w := get(t, h, "GET", "/notes"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "notes/" {
		t.Errorf("a directory without its slash: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get(t, h, "GET", "/notes/a.txt/"); w.Code != http.StatusNotFound {
		t.Errorf("a file with a slash: %d", w.Code)
	}
	if l := listing(t, get(t, h, "GET", "/notes/")); len(l.Entries) != 3 {
		t.Errorf("notes/ = %+v", l.Entries)
	}
	if w := get(t, h, "GET", "/nothing"); w.Code != http.StatusNotFound {
		t.Errorf("missing: %d", w.Code)
	}
}

func TestNothingOutsideTheRootIsReached(t *testing.T) {
	root, _ := tree(t)
	h := Handler{Root: root}
	for _, target := range []string{"/out", "/in", "/notes/up/", "/notes/up/secret", "/notes/up/page.html", "/notes/up/notes/", "/../secret", "/notes/../../secret", "/%2e%2e/secret"} {
		w := get(t, h, "GET", target)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "outside") && !strings.Contains(w.Body.String(), "outside the workspace") {
			t.Errorf("%s: %d %q", target, w.Code, w.Body)
		}
	}
	if w := get(t, h, "GET", "/out"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "symbolic link") {
		t.Errorf("a link is refused, and says why: %d %q", w.Code, w.Body)
	}
}

func TestAPipeNeitherBlocksNorIsServed(t *testing.T) {
	root, _ := tree(t)
	if w := get(t, Handler{Root: root}, "GET", "/pipe"); w.Code != http.StatusForbidden {
		t.Errorf("pipe: %d %q", w.Code, w.Body)
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	root, dir := tree(t)
	h := Handler{Root: root}
	for _, method := range []string{"PUT", "POST", "DELETE", "PROPFIND", "MKCOL", "OPTIONS", "PATCH"} {
		w := get(t, h, method, "/notes/a.txt")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %d %q", method, w.Code, w.Header().Get("Allow"))
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); string(data) != "alpha\n" {
		t.Errorf("the file changed: %q", data)
	}
}

func TestABigDirectoryIsCutAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), nil, 0o644)
	}
	root, _ := os.OpenRoot(dir)
	defer root.Close()
	l := listing(t, get(t, Handler{Root: root, Max: 3}, "GET", "/"))
	if !l.Truncated || len(l.Entries) != 3 {
		t.Errorf("truncated %v, %d entries", l.Truncated, len(l.Entries))
	}
	if l := listing(t, get(t, Handler{Root: root, Max: 5}, "GET", "/")); l.Truncated || len(l.Entries) != 5 {
		t.Errorf("exactly the limit: truncated %v, %d entries", l.Truncated, len(l.Entries))
	}
}

func TestServeCarriesConnectionsAndExitsWhenIdle(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644)
	socket := filepath.Join(t.TempDir(), "s.sock")
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), dir, socket, ServeOptions{Idle: 300 * time.Millisecond}) }()

	in, request := io.Pipe()
	out, response := io.Pipe()
	connected := make(chan struct{})
	go func() { _ = Connect(socket, in, response, func() { close(connected) }); response.Close() }()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("connect never reached the server")
	}
	fmt.Fprint(request, "GET /a.txt HTTP/1.1\r\nHost: files\r\n\r\n")
	answer, err := http.ReadResponse(bufio.NewReader(out), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(answer.Body)
	if answer.StatusCode != http.StatusOK || string(body) != "alpha\n" {
		t.Fatalf("%d %q", answer.StatusCode, body)
	}
	time.Sleep(500 * time.Millisecond)
	select {
	case <-served:
		t.Fatal("the server left while a connection was open")
	default:
	}
	request.Close() // the hub leaves: the connection ends, then the server idles out
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not exit once idle")
	}
}

func TestConnectSaysWhenNothingServes(t *testing.T) {
	connectWait = 200 * time.Millisecond
	defer func() { connectWait = 10 * time.Second }()
	err := Connect(filepath.Join(t.TempDir(), "none.sock"), strings.NewReader(""), io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("err = %v", err)
	}
}
