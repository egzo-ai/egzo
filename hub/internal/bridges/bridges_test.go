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
	server := httptest.NewServer(New(h, map[string]string{"discord": secret, "slack": secret + "-slack"}, "https://hub.example.org", nil).Handler())
	t.Cleanup(server.Close)
	r := &rig{t: t, hub: h, server: server, web: web, project: reg.ID, admin: admin}
	r.linkAccount("d-admin", admin)
	return r
}

// linkAccount links a Discord account to a user, as the person confirming a code does.
func (r *rig) linkAccount(account string, u store.User) {
	r.t.Helper()
	code, _, err := r.hub.BridgeCode(ctx, "discord", account, "joe")
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.hub.ConfirmBridgeCode(ctx, u, code); err != nil {
		r.t.Fatal(err)
	}
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
func (r *rig) open(token string, after string) (*http.Response, <-chan hub.BridgeEvent) {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.server.URL+Prefix+"/messages?after="+after, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	stop, cancel := context.WithCancel(ctx)
	r.t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(stop))
	if err != nil {
		r.t.Fatal(err)
	}
	lines := make(chan hub.BridgeEvent, 16)
	if resp.StatusCode == 200 {
		go func() {
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				var m hub.BridgeEvent
				if strings.TrimSpace(scanner.Text()) != "" && json.Unmarshal(scanner.Bytes(), &m) == nil && m.Type == hub.EventMessage {
					lines <- m
				}
			}
		}()
	}
	return resp, lines
}

func next(t *testing.T, lines <-chan hub.BridgeEvent) hub.BridgeEvent {
	t.Helper()
	select {
	case m := <-lines:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("nothing came")
	}
	return hub.BridgeEvent{}
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
	code, out := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "deploy it", Key: "d-1"})
	if code != 200 || out["id"] == "" {
		t.Fatalf("deliver = %d %v", code, out)
	}
	calls := r.web.Calls()
	if last := calls[len(calls)-1]; last != "send coder-1 user:"+r.admin.ID+" discord:d-1" {
		t.Errorf("the project got %q", last)
	}
	if code, again := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "deploy it", Key: "d-1"}); code != 200 || again["id"] != out["id"] {
		t.Errorf("a retry is the same message: %d %v", code, again)
	}
	if code, out := r.post(secret, Inbound{Project: r.project, Agent: "coder-1", Account: "d-nobody", Text: "x", Key: "d-2"}); code != http.StatusForbidden || out["error"] != "not delivered" {
		t.Errorf("an unknown user = %d %v", code, out)
	}
	for _, bad := range []Inbound{
		{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "no key"},
		{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "an answer without a key", Answer: "mq"},
		{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "x", Key: strings.Repeat("k", 96)},
		{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "x", Key: "with space"},
		{Project: r.project, Agent: "Bad_Name", Account: "d-admin", Text: "x", Key: "k"},
		{Project: "../x", Agent: "coder-1", Account: "d-admin", Text: "x", Key: "k"},
		{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "  ", Key: "k"},
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

func TestAnAnswerIsTriedAgainSafelyAndASecondOneSaysItWasAnsweredAlready(t *testing.T) {
	r := newRig(t)
	click := func(account, key, text string) (int, map[string]string) {
		return r.post(secret, Inbound{Project: r.project, Agent: "coder-1", Account: account, Text: text, Answer: "mq", Key: key})
	}
	if code, out := click("d-admin", "i-1", "staging"); code != http.StatusOK {
		t.Fatalf("the answer = %d %v", code, out)
	}
	if code, out := click("d-admin", "i-1", "staging"); code != http.StatusOK {
		t.Errorf("the answer tried again = %d %v, want it delivered", code, out)
	}
	if code, out := click("d-admin", "i-2", "production"); code != http.StatusConflict || out["error"] != "already answered" {
		t.Errorf("a second answer = %d %v, want 409 already answered", code, out)
	}
	if code, out := click("d-nobody", "i-3", "production"); code != http.StatusForbidden || out["error"] != "not delivered" {
		t.Errorf("someone else = %d %v: a refusal still says nothing of why", code, out)
	}
}

func TestAMessageTheProjectTookButDidNotConfirmInTimeIsSentAgainSafely(t *testing.T) {
	r := newRig(t)
	r.web.LoseReplies(1)
	in := Inbound{Project: r.project, Agent: "coder-1", Account: "d-admin", Text: "deploy it", Key: "d-9"}
	if code, out := r.post(secret, in); code != http.StatusGatewayTimeout || out["error"] != "not confirmed" {
		t.Fatalf("a reply that was lost = %d %v, want 504 not confirmed", code, out)
	}
	code, out := r.post(secret, in)
	if code != http.StatusOK || out["id"] == "" {
		t.Fatalf("sent again = %d %v", code, out)
	}
	var sends int
	for _, call := range r.web.Calls() {
		if strings.HasPrefix(call, "send coder-1 ") && strings.HasSuffix(call, " discord:d-9") {
			sends++
		}
	}
	if sends != 1 {
		t.Errorf("the project got the message %d times, want once", sends)
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
	// another bridge reads from its own place, and never what goes to a person on the first: only what is everybody's, such as what a
	// person said to an agent
	_, slack := r.open(secret+"-slack", "0")
	r.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "user:" + r.admin.ID, To: "agent:coder-1", Kind: "resolution", Text: "staging", Re: "m1", ID: "m4"})
	for {
		m := next(t, slack)
		if m.Type != hub.EventMessage {
			continue // the account linked
		}
		if m.ID != "m4" {
			t.Errorf("slack read %+v", m)
		}
		break
	}
}

func (r *rig) call(method, path string, body any, out any) int {
	r.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, r.server.URL+Prefix+path, strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestABridgeGetsACodeAPersonConfirmsAndThenKnowsTheirAccount(t *testing.T) {
	r := newRig(t)
	var made struct{ Code, URL string }
	if code := r.call("POST", "/codes", map[string]string{"account": "d-joe", "name": "joe"}, &made); code != 200 ||
		!strings.HasPrefix(made.URL, "https://hub.example.org"+LinkPath+"#egzo-chat-") || !strings.HasSuffix(made.URL, made.Code) {
		t.Fatalf("code = %d %+v", code, made)
	}
	if _, err := r.hub.ConfirmBridgeCode(ctx, r.admin, made.Code); err != nil { // signed in on the hub, at that address
		t.Fatal(err)
	}
	var list []Account
	if code := r.call("GET", "/accounts", nil, &list); code != 200 || len(list) != 2 {
		t.Fatalf("accounts = %d %+v", code, list)
	}
	found := false
	for _, a := range list {
		if a.Account == "d-joe" && a.User == r.admin.ID && a.Username == "admin" && a.Name == "joe" {
			found = true
		}
	}
	if !found {
		t.Errorf("d-joe is not listed: %+v", list)
	}
}

func TestABridgeAsksWhichAgentsAPersonMayChatWithAndWhetherTheyMayAddOne(t *testing.T) {
	r := newRig(t)
	var agents []hub.ChatAgent
	if code := r.call("GET", "/agents?account=d-admin", nil, &agents); code != 200 || len(agents) != 1 || agents[0].Agent != "coder-1" || !agents[0].Admin {
		t.Errorf("agents = %d %+v", code, agents)
	}
	if code := r.call("GET", "/agents?account=d-nobody", nil, nil); code != http.StatusForbidden {
		t.Errorf("an account nobody linked = %d", code)
	}
	var may struct{ Allowed bool }
	if code := r.call("POST", "/may-add", map[string]string{"account": "d-admin", "project": r.project, "agent": "coder-1"}, &may); code != 200 || !may.Allowed {
		t.Errorf("the owner = %d %+v", code, may)
	}
	may.Allowed = true
	if code := r.call("POST", "/may-add", map[string]string{"account": "d-nobody", "project": r.project, "agent": "coder-1"}, &may); code != 200 || may.Allowed {
		t.Errorf("nobody = %d %+v", code, may)
	}
}

func TestABridgeAsksWhetherAgentsAreStillInTheirProjects(t *testing.T) {
	r := newRig(t)
	var out struct{ Agents []hub.AgentPresence }
	body := map[string]any{"agents": []hub.AgentRef{{Project: r.project, Agent: "coder-1"}, {Project: r.project, Agent: "coder-9"}}}
	if code := r.call("POST", "/presence", body, &out); code != 200 || len(out.Agents) != 2 ||
		out.Agents[0].State != hub.PresencePresent || out.Agents[1].State != hub.PresenceAbsent || out.Agents[1].Agent != "coder-9" {
		t.Errorf("presence = %d %+v", code, out)
	}
	if code := r.call("POST", "/presence", map[string]any{"agents": []hub.AgentRef{{Project: r.project, Agent: "../x"}}}, nil); code != http.StatusBadRequest {
		t.Errorf("a bad name = %d", code)
	}
	many := make([]hub.AgentRef, 1001)
	for i := range many {
		many[i] = hub.AgentRef{Project: r.project, Agent: "coder-1"}
	}
	if code := r.call("POST", "/presence", map[string]any{"agents": many}, nil); code != http.StatusBadRequest {
		t.Errorf("too many = %d", code)
	}
}

func TestABridgeSaysAMessageItWasGivenDidNotReachThePersonAndTheAgentIsTold(t *testing.T) {
	r := newRig(t)
	_, lines := r.open(secret, "-1")
	r.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + r.admin.ID, Kind: "question", Text: "staging?", ID: "m1"})
	if m := next(t, lines); m.ID != "m1" || m.Bridge != "discord" || m.Account != "d-admin" {
		t.Fatalf("read %+v", m)
	}
	report := func(token, id string, body any) int {
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", r.server.URL+Prefix+"/messages/"+id+"/undelivered", strings.NewReader(string(data)))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		token, id string
		body      any
		want      int
	}{
		{secret, "m1", map[string]string{"account": "d-admin"}, http.StatusBadRequest},
		{secret, "m1", map[string]string{"account": "d-other", "code": "refused"}, http.StatusNotFound},
		{secret + "-slack", "m1", map[string]string{"account": "d-admin", "code": "refused"}, http.StatusNotFound},
		{"nope", "m1", map[string]string{"account": "d-admin", "code": "refused"}, http.StatusUnauthorized},
		{secret, "m1", map[string]string{"account": "d-admin", "code": "person-unreachable", "detail": "50007"}, http.StatusNoContent},
	} {
		if got := report(c.token, c.id, c.body); got != c.want {
			t.Errorf("%v: %d, want %d", c.body, got, c.want)
		}
	}
	calls := r.web.Calls()
	if last := calls[len(calls)-1]; last != "undelivered m1 admin cannot be reached on Discord" {
		t.Errorf("told %q", last)
	}
}
