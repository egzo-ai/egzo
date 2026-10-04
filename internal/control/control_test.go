package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	r.agent = httptest.NewServer((&agentAPI{server: srv}).handler())
	r.operator = httptest.NewServer(srv.handler())
	t.Cleanup(r.agent.Close)
	t.Cleanup(r.operator.Close)
	return r
}

func (r *rig) token(agent string) string {
	key, err := r.srv.projectKey()
	if err != nil {
		r.t.Fatal(err)
	}
	return AgentToken(key, agent)
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
	if r.token("coder") != r.token("coder") {
		t.Error("a token changed between calls")
	}
	if r.token("coder") == r.token("reviewer") {
		t.Error("two agents share a token")
	}
	again, _ := newServer(r.srv.dir) // a restarted sidecar on the same volume
	key, _ := again.projectKey()
	if AgentToken(key, "coder") != r.token("coder") {
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
	for _, path := range []string{"/events", "/queue", "/questions", "/agents", "/specs", "/tokens/coder"} {
		response := r.asAgent("coder", "GET", path, "")
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s on the agent port = %d: operator verbs must not be reachable by agents", path, response.StatusCode)
		}
	}
}

func TestStatusAndSayBecomeAttributedEvents(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"refactoring"}`).Body.Close()
	r.asAgent("coder", "POST", "/v1/say", `{"text":"done with step one"}`).Body.Close()

	events := r.srv.events.all()
	if len(events) != 2 || events[0].Type != "status" || events[1].Type != "say" {
		t.Fatalf("events = %+v", events)
	}
	for _, event := range events {
		if event.Agent != "coder" || event.Actor != "agent:coder" || event.Seq == 0 || event.Time.IsZero() {
			t.Errorf("event is not attributed: %+v", event)
		}
	}
	var statuses []AgentStatus
	decode(t, r.asOperator("GET", "/agents", ""), &statuses)
	if len(statuses) != 1 || statuses[0].Status != "refactoring" {
		t.Errorf("statuses = %+v", statuses)
	}
}

func TestEmptyAndOversizedTextIsRefused(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string]string{
		"empty":     `{"text":"  "}`,
		"not json":  `hello`,
		"too large": `{"text":"` + strings.Repeat("x", maxTextSize+1) + `"}`,
	} {
		response := r.asAgent("coder", "POST", "/v1/say", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, response.StatusCode)
		}
	}
}

func TestAQuestionKeepsItsIdAndIsAnsweredOnce(t *testing.T) {
	r := newRig(t)
	var asked struct{ ID string }
	decode(t, r.asAgent("coder", "POST", "/v1/ask", `{"text":"deploy to prod?"}`), &asked)
	if asked.ID == "" {
		t.Fatal("no question id")
	}

	var open Question
	decode(t, r.asAgent("coder", "GET", "/v1/questions/"+asked.ID, ""), &open)
	if open.Answered {
		t.Error("answered before anyone answered")
	}

	answer := r.asOperator("POST", "/questions/"+asked.ID+"/answer", `{"actor":"user:alice","text":"yes, after the tests pass"}`)
	answer.Body.Close()
	if answer.StatusCode != http.StatusNoContent {
		t.Fatalf("answer status %d", answer.StatusCode)
	}
	var done Question
	decode(t, r.asAgent("coder", "GET", "/v1/questions/"+asked.ID, ""), &done)
	if !done.Answered || done.Answer != "yes, after the tests pass" || done.AnsweredBy != "user:alice" {
		t.Errorf("question = %+v", done)
	}

	again := r.asOperator("POST", "/questions/"+asked.ID+"/answer", `{"text":"changed my mind"}`)
	again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Errorf("answering twice: %d, want 409", again.StatusCode)
	}
	missing := r.asOperator("POST", "/questions/nope/answer", `{"text":"x"}`)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("unknown question: %d, want 404", missing.StatusCode)
	}
}

func TestAnAgentCannotSeeAnotherAgentsQuestion(t *testing.T) {
	r := newRig(t)
	var asked struct{ ID string }
	decode(t, r.asAgent("coder", "POST", "/v1/ask", `{"text":"secret plan?"}`), &asked)
	response := r.asAgent("reviewer", "GET", "/v1/questions/"+asked.ID, "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", response.StatusCode)
	}
}

func TestMessagesAreDeliveredToTheirAgentOnce(t *testing.T) {
	r := newRig(t)
	r.asOperator("POST", "/queue", `{"to":"coder","text":"first"}`).Body.Close()
	r.asOperator("POST", "/queue", `{"to":"coder","from":"user:bob","text":"second"}`).Body.Close()
	r.asOperator("POST", "/queue", `{"to":"reviewer","text":"not for the coder"}`).Body.Close()

	var inbox []Message
	decode(t, r.asAgent("coder", "GET", "/v1/inbox", ""), &inbox)
	if len(inbox) != 2 || inbox[0].Text != "first" || inbox[1].Text != "second" {
		t.Fatalf("inbox = %+v", inbox)
	}
	if inbox[0].From != "operator" || inbox[1].From != "user:bob" {
		t.Errorf("senders = %q, %q", inbox[0].From, inbox[1].From)
	}

	var again []Message
	decode(t, r.asAgent("coder", "GET", "/v1/inbox", ""), &again)
	if len(again) != 0 {
		t.Errorf("delivered messages came back: %+v", again)
	}
	var queued []Message
	decode(t, r.asOperator("GET", "/queue?agent=reviewer", ""), &queued)
	if len(queued) != 1 {
		t.Errorf("the reviewer's message was lost or delivered to the wrong agent: %+v", queued)
	}
}

func TestSendersMustBeValidActors(t *testing.T) {
	r := newRig(t)
	for _, from := range []string{"root", "agent:", "user:bad name", "../x"} {
		response := r.asOperator("POST", "/queue", `{"to":"coder","from":"`+from+`","text":"hi"}`)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("from %q: status %d, want 400", from, response.StatusCode)
		}
	}
}

func TestHooksBecomeEventsWithTheirPayload(t *testing.T) {
	r := newRig(t)
	response := r.asAgent("coder", "POST", "/v1/hooks/Stop", `{"last_assistant_message":"done"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", response.StatusCode)
	}
	events := r.srv.events.all()
	if len(events) < 1 || events[0].Type != "hook" || events[0].Text != "Stop" || !strings.Contains(string(events[0].Data), "done") {
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
	r.asAgent("coder", "POST", "/v1/say", `{"text":"before"}`).Body.Close()
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if events := reopened.events.all(); len(events) != 1 || events[0].Text != "before" {
		t.Fatalf("events after restart = %+v", events)
	}
	event, _ := reopened.events.append(Event{Type: "say", Agent: "coder", Text: "after"})
	if event.Seq != 2 {
		t.Errorf("sequence restarted at %d", event.Seq)
	}
}

func TestFollowingTheStreamDeliversNewEventsAsTheyHappen(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/say", `{"text":"old"}`).Body.Close()

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
	r.asAgent("coder", "POST", "/v1/say", `{"text":"live"}`).Body.Close()
	if got := next(); got.Text != "live" {
		t.Errorf("followed event = %+v", got)
	}
}

func TestEventsCanBeFilteredByAgentAndSequence(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/say", `{"text":"a"}`).Body.Close()
	r.asAgent("reviewer", "POST", "/v1/say", `{"text":"b"}`).Body.Close()
	r.asAgent("coder", "POST", "/v1/say", `{"text":"c"}`).Body.Close()

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

func TestMCPOffersTheAgentVerbsAsTools(t *testing.T) {
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
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "say") || !strings.Contains(got, "ask_user") || !strings.Contains(got, "check_inbox") || !strings.Contains(got, "status") || !strings.Contains(got, "get_answer") {
		t.Errorf("tools = %v", names)
	}
}

func TestMCPToolCallsAreAttributedToTheAuthenticatedAgent(t *testing.T) {
	r := newRig(t)
	session, err := r.mcpSession("coder", r.token("coder"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call(t, session, "say", map[string]any{"text": "hello from mcp"})
	call(t, session, "status", map[string]any{"text": "writing tests"})

	events := r.srv.events.all()
	if len(events) != 2 || events[0].Type != "say" || events[0].Actor != "agent:coder" || events[1].Type != "status" {
		t.Fatalf("events = %+v", events)
	}
}

func TestMCPQuestionsAndInboxWorkEndToEnd(t *testing.T) {
	r := newRig(t)
	session, err := r.mcpSession("coder", r.token("coder"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	asked := call(t, session, "ask_user", map[string]any{"text": "ship it?"})
	var question struct{ ID string }
	raw, _ := json.Marshal(asked.StructuredContent)
	json.Unmarshal(raw, &question)
	if question.ID == "" {
		t.Fatalf("no question id in %s", raw)
	}

	r.asOperator("POST", "/questions/"+question.ID+"/answer", `{"actor":"user:alice","text":"ship it"}`).Body.Close()
	answer := call(t, session, "get_answer", map[string]any{"id": question.ID})
	raw, _ = json.Marshal(answer.StructuredContent)
	if !strings.Contains(string(raw), `"answered":true`) || !strings.Contains(string(raw), "ship it") {
		t.Errorf("answer = %s", raw)
	}

	r.asOperator("POST", "/queue", `{"to":"coder","text":"please rebase"}`).Body.Close()
	inbox := call(t, session, "check_inbox", nil)
	raw, _ = json.Marshal(inbox.StructuredContent)
	if !strings.Contains(string(raw), "please rebase") {
		t.Errorf("inbox = %s", raw)
	}
	again := call(t, session, "check_inbox", nil)
	raw, _ = json.Marshal(again.StructuredContent)
	if strings.Contains(string(raw), "please rebase") {
		t.Errorf("a delivered message came back: %s", raw)
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

func TestMCPAgentsCannotReadEachOthersQuestions(t *testing.T) {
	r := newRig(t)
	coder, _ := r.mcpSession("coder", r.token("coder"))
	reviewer, _ := r.mcpSession("reviewer", r.token("reviewer"))
	defer coder.Close()
	defer reviewer.Close()
	asked := call(t, coder, "ask_user", map[string]any{"text": "private?"})
	var question struct{ ID string }
	raw, _ := json.Marshal(asked.StructuredContent)
	json.Unmarshal(raw, &question)

	result, err := reviewer.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_answer", Arguments: map[string]any{"id": question.ID}})
	if err == nil && !result.IsError {
		t.Error("an agent read another agent's question")
	}
}

func TestMCPRejectsEmptyText(t *testing.T) {
	r := newRig(t)
	session, _ := r.mcpSession("coder", r.token("coder"))
	defer session.Close()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "say", Arguments: map[string]any{"text": "  "}})
	if err == nil && !result.IsError {
		t.Error("an empty message was accepted")
	}
	if len(r.srv.events.all()) != 0 {
		t.Error("an event was recorded for an empty message")
	}
}
