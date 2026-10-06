package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rig struct {
	t        *testing.T
	srv      *server
	agent    *httptest.Server
	operator *httptest.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, srv: srv}
	for _, name := range []string{"coder", "reviewer"} {
		r.register(name)
	}
	r.agent = httptest.NewServer((&agentAPI{server: srv}).handler())
	r.operator = httptest.NewServer(srv.handler())
	t.Cleanup(r.agent.Close)
	t.Cleanup(r.operator.Close)
	return r
}

// register makes an agent known to the sidecar, as spawning an instance does.
func (r *rig) register(name string) {
	r.t.Helper()
	response := httptest.NewRecorder()
	r.srv.handler().ServeHTTP(response, httptest.NewRequest("PUT", "/agents/"+name, nil))
	if response.Code != http.StatusNoContent {
		r.t.Fatalf("register %s: status %d", name, response.Code)
	}
}

func (r *rig) token(agent string) string {
	token, err := r.srv.agentToken(agent)
	if err != nil {
		r.t.Fatal(err)
	}
	return token
}

func (r *rig) asAgent(agent, method, path, body string) *http.Response {
	r.t.Helper()
	request, _ := http.NewRequest(method, r.agent.URL+path, strings.NewReader(body))
	request.SetBasicAuth(agent, r.token(agent))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.t.Fatal(err)
	}
	return response
}

func (r *rig) asOperator(method, path, body string) *http.Response {
	r.t.Helper()
	request, _ := http.NewRequest(method, r.operator.URL+path, strings.NewReader(body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.t.Fatal(err)
	}
	return response
}

func decode(t *testing.T, response *http.Response, into any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}

func TestTokensAreStablePerAgentAndDifferBetweenAgents(t *testing.T) {
	r := newRig(t)
	first := r.token("coder")
	if first != r.token("coder") {
		t.Error("a token changed between calls")
	}
	if r.token("coder") == r.token("reviewer") {
		t.Error("two agents share a token")
	}
	again, _ := newServer(r.srv.dir) // a restarted sidecar on the same volume
	if token, _ := again.agentToken("coder"); token != r.token("coder") {
		t.Error("tokens did not survive a restart of the sidecar")
	}
}

func TestAgentsAuthenticateWithTheirOwnToken(t *testing.T) {
	r := newRig(t)
	post := func(user, password string) int {
		request, _ := http.NewRequest("POST", r.agent.URL+"/v1/status", strings.NewReader(`{"text":"x"}`))
		if user != "" {
			request.SetBasicAuth(user, password)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if got := post("", ""); got != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", got)
	}
	if got := post("coder", "wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", got)
	}
	if got := post("coder", r.token("reviewer")); got != http.StatusUnauthorized {
		t.Errorf("another agent's token: %d", got)
	}
	if got := post("../etc", "x"); got != http.StatusUnauthorized {
		t.Errorf("unsafe agent name: %d", got)
	}
	if got := post("coder", r.token("coder")); got != http.StatusNoContent {
		t.Errorf("valid credentials: %d", got)
	}
}

func TestTheAgentPortServesNoOperatorVerbs(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/events", "/queue", "/messages", "/questions", "/agents", "/specs", "/tokens/coder"} {
		response := r.asAgent("coder", "GET", path, "")
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s on the agent port = %d: operator verbs must not be reachable by agents", path, response.StatusCode)
		}
	}
}

func TestStatusBecomesAnAttributedEvent(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"refactoring"}`).Body.Close()
	events := r.srv.events.all()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if e := events[0]; e.Type != "status" || e.Agent != "coder" || e.Actor != "agent:coder" || e.Text != "refactoring" || e.Seq != 1 || e.Time.IsZero() {
		t.Errorf("event = %+v", e)
	}
}

func TestEmptyAndOversizedTextIsRefused(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{`{"text":""}`, `{"text":"   "}`, `{"text":"` + strings.Repeat("x", maxTextSize+1) + `"}`, `not json`} {
		response := r.asAgent("coder", "POST", "/v1/status", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status for %.30q = %d, want 400", body, response.StatusCode)
		}
	}
	if len(r.srv.events.all()) != 0 {
		t.Error("events were recorded for refused input")
	}
}

func TestHooksBecomeEventsWithTheNameOfWhatHappened(t *testing.T) {
	r := newRig(t)
	response := r.asAgent("coder", "POST", "/v1/hooks/PreToolUse", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", response.StatusCode)
	}
	events := r.srv.events.all()
	if len(events) < 1 || events[0].Type != "hook" || events[0].Text != "PreToolUse" || !strings.Contains(string(events[0].Data), "Bash") {
		t.Errorf("events = %+v", events)
	}
	bad := r.asAgent("coder", "POST", "/v1/hooks/..%2Fx", `{}`)
	bad.Body.Close()
	if bad.StatusCode == http.StatusNoContent {
		t.Error("an unsafe hook name was accepted")
	}
}

func TestTheEventLogSurvivesARestart(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"before"}`).Body.Close()
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if events := reopened.events.all(); len(events) != 1 || events[0].Text != "before" {
		t.Fatalf("events after restart = %+v", events)
	}
	event, _ := reopened.events.append(Event{Type: "status", Agent: "coder", Text: "after"})
	if event.Seq != 2 {
		t.Errorf("sequence restarted at %d", event.Seq)
	}
}

func TestFollowingTheStreamDeliversNewEventsAsTheyHappen(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"old"}`).Body.Close()

	response := r.asOperator("GET", "/events?follow=1", "")
	defer response.Body.Close()
	lines := bufio.NewScanner(response.Body)
	next := func() Event {
		done := make(chan Event, 1)
		go func() {
			if lines.Scan() {
				var event Event
				json.Unmarshal(lines.Bytes(), &event)
				done <- event
			}
		}()
		select {
		case event := <-done:
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("no event arrived")
			return Event{}
		}
	}
	if got := next(); got.Text != "old" {
		t.Errorf("first event = %+v", got)
	}
	r.asAgent("coder", "POST", "/v1/status", `{"text":"live"}`).Body.Close()
	if got := next(); got.Text != "live" {
		t.Errorf("followed event = %+v", got)
	}
}

func TestEventsCanBeFilteredByAgentAndSequence(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"a"}`).Body.Close()
	r.asAgent("reviewer", "POST", "/v1/status", `{"text":"b"}`).Body.Close()
	r.asAgent("coder", "POST", "/v1/status", `{"text":"c"}`).Body.Close()

	read := func(query string) []string {
		response := r.asOperator("GET", "/events?"+query, "")
		defer response.Body.Close()
		var texts []string
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			var event Event
			json.Unmarshal(scanner.Bytes(), &event)
			texts = append(texts, event.Text)
		}
		return texts
	}
	if got := strings.Join(read("agent=coder"), ""); got != "ac" {
		t.Errorf("agent filter = %q", got)
	}
	if got := strings.Join(read("after=2"), ""); got != "c" {
		t.Errorf("after filter = %q", got)
	}
}

type basicAuth struct {
	user, password string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.password)
	return http.DefaultTransport.RoundTrip(r)
}

func (r *rig) mcpSession(agent, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "spec-client", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   r.agent.URL + "/mcp",
		HTTPClient: &http.Client{Transport: basicAuth{agent, token}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Connect(ctx, transport, nil)
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return result
}

func TestMCPOffersExactlyTheMessageTools(t *testing.T) {
	r := newRig(t)
	session, err := r.mcpSession("coder", r.token("coder"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %s has no description for the model to read", tool.Name)
		}
		if strings.Contains(tool.Description, "sparingly") {
			t.Errorf("tool %s tells the model to use it sparingly", tool.Name)
		}
	}
	sort.Strings(names)
	want := []string{"agents", "ask", "get_message", "list_messages", "message", "resolve", "status", "update"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestMCPRefusesAgentsWithoutValidCredentials(t *testing.T) {
	r := newRig(t)
	if session, err := r.mcpSession("coder", "wrong-token"); err == nil {
		session.Close()
		t.Fatal("connected to MCP with a wrong token")
	}
	if session, err := r.mcpSession("coder", r.token("reviewer")); err == nil {
		session.Close()
		t.Fatal("connected to MCP with another agent's token")
	}
}
