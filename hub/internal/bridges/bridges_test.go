// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package bridges

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var ctx = context.Background()

const secret = "discord-secret-of-at-least-32-characters"

type rig struct {
	t       *testing.T
	hub     *hub.Hub
	server  *httptest.Server
	web     *fake.Engine
	project string
	admin   store.User
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := hub.New(s, nil, hub.FeedOptions{Flush: 5 * time.Millisecond})
	if _, err := h.SetupSignIn(ctx, hub.SignInOptions{LocalLogin: true, AdminPassword: "admin-password"}); err != nil {
		t.Fatal(err)
	}
	admin, err := s.UserByName(ctx, store.StarterUsername)
	if err != nil {
		t.Fatal(err)
	}
	_, code, err := h.CreateLinkCode(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	web := fake.New("web", "laptop", "coder")
	go h.Link(ctx, web.Hello(), code, web.Connect())
	t.Cleanup(web.Drop)
	waitFor(t, "the link to hear it", web.Served)
	web.SetAgent(links.Agent{Name: "coder-1", Template: "coder", State: "running", Actor: "operator", Created: time.Now()})
	reg, err := s.RegistrationByKey(ctx, web.Hello().Key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(h, map[string]string{"discord": secret, "slack": secret + "-slack"}, nil).Handler())
	t.Cleanup(server.Close)
	return &rig{t: t, hub: h, server: server, web: web, project: reg.ID, admin: admin}
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waited for %s", what)
}

func (r *rig) post(token string, in Inbound) (int, map[string]string) {
	r.t.Helper()
	body, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", r.server.URL+Prefix+"/messages", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// open reads the stream after seq, line by line, until the test ends.
func (r *rig) open(token string, after string) (*http.Response, <-chan hub.BridgeMessage) {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.server.URL+Prefix+"/messages?after="+after, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	stop, cancel := context.WithCancel(ctx)
	r.t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(stop))
	if err != nil {
		r.t.Fatal(err)
	}
	lines := make(chan hub.BridgeMessage, 16)
	if resp.StatusCode == 200 {
		go func() {
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				var m hub.BridgeMessage
				if strings.TrimSpace(scanner.Text()) != "" && json.Unmarshal(scanner.Bytes(), &m) == nil {
					lines <- m
				}
			}
		}()
	}
	return resp, lines
}

func next(t *testing.T, lines <-chan hub.BridgeMessage) hub.BridgeMessage {
	t.Helper()
	select {
	case m := <-lines:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("nothing came")
	}
	return hub.BridgeMessage{}
}

func TestABridgeIsKnownByItsTokenAndNothingElse(t *testing.T) {
	r := newRig(t)
	for _, token := range []string{"", "wrong", secret + "x"} {
		if code, _ := r.post(token, Inbound{}); code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", token, code)
		}
		if resp, _ := r.open(token, "0"); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("stream with token %q: %d", token, resp.StatusCode)
		}
	}
}

func TestWhatABridgePostsIsDeliveredUnderItsNameAndARefusalSaysNothingOfWhy(t *testing.T) {
	r := newRig(t)
	code, out := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", User: r.admin.ID, Text: "deploy it", Key: "d-1"})
	if code != 200 || out["id"] == "" {
		t.Fatalf("deliver = %d %v", code, out)
	}
	calls := r.web.Calls()
	if last := calls[len(calls)-1]; last != "send coder-1 user:"+r.admin.ID+" d-1" {
		t.Errorf("the project got %q", last)
	}
	if code, again := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", User: r.admin.ID, Text: "deploy it", Key: "d-1"}); code != 200 || again["id"] != out["id"] {
		t.Errorf("a retry is the same message: %d %v", code, again)
	}
	if code, out := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", User: "u-nobody", Text: "x", Key: "d-2"}); code != http.StatusForbidden || out["error"] != "not delivered" {
		t.Errorf("an unknown user = %d %v", code, out)
	}
	for _, bad := range []Inbound{
		{Project: r.project, Agent: "coder-1", User: r.admin.ID, Text: "no key"},
		{Project: r.project, Agent: "Bad_Name", User: r.admin.ID, Text: "x", Key: "k"},
		{Project: "../x", Agent: "coder-1", User: r.admin.ID, Text: "x", Key: "k"},
		{Project: r.project, Agent: "coder-1", User: r.admin.ID, Text: "  ", Key: "k"},
	} {
		if code, _ := r.post(secret, bad); code != http.StatusBadRequest {
			t.Errorf("%+v: %d", bad, code)
		}
	}
	entries, _ := r.hub.Store().AuditOf(ctx, r.project, 50)
	var via []string
	for _, e := range entries {
		if e.Action == "agent.message" {
			via = append(via, e.Detail)
		}
	}
	if len(via) != 3 || !strings.Contains(strings.Join(via, "|"), "via discord") {
		t.Errorf("audited %q", via)
	}
}

func TestTheStreamGivesWhatComesAndABridgeResumesAfterTheLastItHandled(t *testing.T) {
	was := keepAlive
	keepAlive = 20 * time.Millisecond
	t.Cleanup(func() { keepAlive = was })
	r := newRig(t)
	resp, lines := r.open(secret, "-1")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	r.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + r.admin.ID, Kind: "question", Text: "staging?", ID: "m1"})
	r.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "agent:peer-1", Kind: "request", ID: "m2"})
	r.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + r.admin.ID, Kind: "update", Text: "on it", ID: "m3"})
	first, second := next(t, lines), next(t, lines)
	if first.Text != "staging?" || first.Project != r.project || second.Text != "on it" || second.Seq <= first.Seq {
		t.Fatalf("read %+v then %+v", first, second)
	}
	time.Sleep(60 * time.Millisecond) // keepalives in between are skipped

	_, again := r.open(secret, strconv.FormatInt(first.Seq, 10))
	if m := next(t, again); m.ID != "m3" {
		t.Errorf("resumed with %+v, want the update", m)
	}
	_, slack := r.open(secret+"-slack", "0")
	if m := next(t, slack); m.ID != "m1" {
		t.Errorf("another bridge reads from its own place: %+v", m)
	}
}
