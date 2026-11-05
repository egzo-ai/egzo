// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

func TestAFilesAttemptThatNeverReachedTheServerReadsNothingOfTheHub(t *testing.T) {
	hub := strings.NewReader("GET / HTTP/1.1\r\n\r\n")
	var said bytes.Buffer
	first := newFilesAttempt(hub, io.Discard, &said)
	read := make(chan string, 1)
	go func() { data, _ := io.ReadAll(first.in); read <- string(data) }() // the exec's stdin copy, blocked until the end
	first.said.Write([]byte("the file server does not answer\n"))
	time.Sleep(50 * time.Millisecond)
	first.end()
	select {
	case data := <-read:
		if data != "" {
			t.Errorf("the failed attempt read %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failed attempt's reader never let go")
	}
	if first.crossed() {
		t.Error("crossed, though the server was never reached")
	}

	second := newFilesAttempt(hub, io.Discard, &said)
	second.said.Write([]byte("connected\n"))
	if data, _ := io.ReadAll(second.in); string(data) != "GET / HTTP/1.1\r\n\r\n" {
		t.Errorf("the next attempt read %q", data)
	}
	second.end()
	if !second.crossed() {
		t.Error("the announce says the server was reached")
	}
	if !strings.Contains(said.String(), "connected") || !strings.Contains(said.String(), "does not answer") {
		t.Errorf("what the command said is passed on: %q", said.String())
	}
}

func TestAByteOfTheServerSaysTheAttemptCrossed(t *testing.T) {
	var out bytes.Buffer
	a := newFilesAttempt(strings.NewReader(""), &out, io.Discard)
	a.out.Write([]byte("HTTP/1.1 200 OK\r\n"))
	a.end()
	if !a.crossed() || out.String() != "HTTP/1.1 200 OK\r\n" {
		t.Errorf("crossed %v, out %q", a.crossed(), out.String())
	}
}

func TestTheRemotesShownAreHTTPSURLsWithNothingThatCouldBeASecret(t *testing.T) {
	git := func(u, branch string) config.ResolvedWorkspace {
		return config.ResolvedWorkspace{Git: &config.Git{URL: u, Branch: branch}}
	}
	published := stack.Published{Templates: map[string]stack.Template{
		"coder": {Workspaces: map[string]config.ResolvedWorkspace{
			"repo":   git("https://github.com/egzo-ai/egzo.git", "main"),
			"docs":   {},
			"secret": git("https://user:token@example.com/r.git?access_token=x#frag", ""),
		}},
		"other": {Workspaces: map[string]config.ResolvedWorkspace{
			"repo":  git("https://github.com/egzo-ai/egzo.git", "main"),
			"ssh":   git("ssh://git@example.com/r.git", ""),
			"plain": git("http://example.com/r.git", ""),
			"bad":   git("https://%zz", ""),
		}},
	}}
	got := remotesOf(published)
	want := map[string]config.Git{
		"repo":   {URL: "https://github.com/egzo-ai/egzo.git", Branch: "main"},
		"secret": {URL: "https://example.com/r.git"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for name, g := range want {
		if got[name] != g {
			t.Errorf("%s = %+v, want %+v", name, got[name], g)
		}
	}
}
