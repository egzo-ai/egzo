package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.String()
		}
	}
}

// The plain HTTP verbs are the same verbs the MCP tools offer: each is exercised through the agent port.
func TestTheHTTPVerbsWalkARequestFromQueuedToResolved(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	id := r.request("coder", "please review")

	got := r.asAgent("coder", "GET", "/v1/messages/"+id, "")
	var message Message
	decode(t, got, &message)
	if got.StatusCode != 200 || message.Text != "please review" || message.State != stateFetched {
		t.Fatalf("get: %d %+v", got.StatusCode, message)
	}
	var listed []Message
	decode(t, r.asAgent("coder", "GET", "/v1/messages", ""), &listed)
	if len(listed) != 1 || listed[0].ID != id {
		t.Errorf("list = %+v", listed)
	}
	var updated map[string]string
	response := r.asAgent("coder", "POST", "/v1/messages/"+id+"/update", `{"text":"half way"}`)
	decode(t, response, &updated)
	if response.StatusCode != http.StatusCreated || updated["id"] == "" {
		t.Errorf("update: %d %v", response.StatusCode, updated)
	}
	asked := r.asAgent("coder", "POST", "/v1/messages/"+id+"/ask", `{"text":"which branch?","choices":["main","dev"]}`)
	if asked.StatusCode != http.StatusCreated {
		t.Errorf("ask: %d %s", asked.StatusCode, body(t, asked))
	}
	var others []AgentStatus
	decode(t, r.asAgent("coder", "GET", "/v1/agents", ""), &others)
	if len(others) != 1 || others[0].Agent != "reviewer" {
		t.Errorf("agents = %+v", others)
	}
	sent := r.asAgent("coder", "POST", "/v1/messages", `{"to":"agent:reviewer","text":"take a look"}`)
	if sent.StatusCode != http.StatusCreated {
		t.Errorf("send: %d %s", sent.StatusCode, body(t, sent))
	}
	resolve := r.asAgent("coder", "POST", "/v1/messages/"+id+"/resolve", `{"text":"done","outcome":"done"}`)
	if resolve.StatusCode != http.StatusNoContent {
		t.Errorf("resolve: %d %s", resolve.StatusCode, body(t, resolve))
	}
	again := r.asAgent("coder", "POST", "/v1/messages/"+id+"/resolve", `{"text":"done","outcome":"done"}`)
	if again.StatusCode != http.StatusConflict {
		t.Errorf("resolving twice: %d", again.StatusCode)
	}
}

func TestTheHTTPVerbsRefuseWhatIsNotTheCallersOrNotWellFormed(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	id := r.request("coder", "private")
	for _, path := range []string{"/v1/messages/" + id, "/v1/messages/" + id + "/resolve", "/v1/messages/m" + strings.Repeat("0", 32)} {
		method := "GET"
		if strings.HasSuffix(path, "resolve") {
			method = "POST"
		}
		response := r.asAgent("reviewer", method, path, `{"text":"x","outcome":"done"}`)
		text := body(t, response)
		if response.StatusCode != http.StatusNotFound || !strings.Contains(text, "no such message") {
			t.Errorf("%s %s as someone else: %d %q", method, path, response.StatusCode, text)
		}
	}
	for name, request := range map[string]struct{ path, body string }{
		"not json":      {"/v1/messages", "not json"},
		"unknown to":    {"/v1/messages", `{"to":"nobody","text":"x"}`},
		"no such agent": {"/v1/messages", `{"to":"agent:ghost","text":"x"}`},
		"empty text":    {"/v1/messages", `{"to":"operator","text":" "}`},
		"bad outcome":   {"/v1/messages/" + id + "/resolve", `{"text":"x","outcome":"maybe"}`},
		"unfetched ask": {"/v1/messages/" + id + "/ask", `{"text":"x"}`},
		"unfetched upd": {"/v1/messages/" + id + "/update", `{"text":"x"}`},
	} {
		response := r.asAgent("coder", "POST", request.path, request.body)
		if response.StatusCode < 400 || response.StatusCode >= 500 {
			t.Errorf("%s: status %d", name, response.StatusCode)
		}
		response.Body.Close()
	}
}

func TestTheOperatorVerbs(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	token := body(t, r.asOperator("GET", "/tokens/coder", ""))
	if token != r.token("coder") {
		t.Error("the token endpoint does not give the agent's token")
	}
	if response := r.asOperator("GET", "/tokens/..%2Fx", ""); response.StatusCode == http.StatusOK {
		t.Error("an unsafe agent name was given a token")
	}

	// an agent asks the operator, the operator answers
	r.srv.sendFromAgent("coder", "operator", "which one?", "")
	var rows []Message
	decode(t, r.asOperator("GET", "/messages?kind=request", ""), &rows)
	if len(rows) != 1 {
		t.Fatalf("messages = %+v", rows)
	}
	answer := r.asOperator("POST", "/messages/"+rows[0].ID+"/resolve", `{"text":"the first","outcome":"done"}`)
	if answer.StatusCode != http.StatusNoContent {
		t.Errorf("resolve: %d %s", answer.StatusCode, body(t, answer))
	}
	if response := r.asOperator("POST", "/messages/"+rows[0].ID+"/resolve", `{"text":"again"}`); response.StatusCode != http.StatusConflict {
		t.Errorf("resolving twice: %d", response.StatusCode)
	}
	if response := r.asOperator("POST", "/messages/nope/resolve", `{"text":"x"}`); response.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: %d", response.StatusCode)
	}
	if response := r.asOperator("POST", "/messages/"+rows[0].ID+"/resolve", `{"actor":"agent:coder","text":"x"}`); response.StatusCode != http.StatusBadRequest {
		t.Errorf("an agent address as the actor of a person: %d", response.StatusCode)
	}

	if response := r.asOperator("PUT", "/agents/zzz", ""); response.StatusCode != http.StatusNoContent || !r.srv.knownAgent("zzz") {
		t.Errorf("register: %d", response.StatusCode)
	}
	if r.srv.knownAgent("never") || r.srv.knownAgent("../key") || r.srv.knownAgent("") {
		t.Error("knownAgent accepts what was never registered")
	}
}

func TestListSpecsAndTheHealthEndpoint(t *testing.T) {
	r := newRig(t)
	one, two := "a: 1\n", "b: 2\n"
	r.asOperator("PUT", "/specs/"+hashOf(one), one).Body.Close()
	r.asOperator("PUT", "/specs/"+hashOf(two), two).Body.Close()
	names := strings.Fields(body(t, r.asOperator("GET", "/specs", "")))
	if len(names) != 2 || (names[0] != hashOf(one) && names[0] != hashOf(two)) {
		t.Errorf("specs = %v", names)
	}
	if response := r.asOperator("GET", "/healthz", ""); response.StatusCode != 200 {
		t.Errorf("healthz = %d", response.StatusCode)
	}
	for _, bad := range []string{"..", "a%2F..", "x y"} {
		if response := r.asOperator("GET", "/specs/"+bad, ""); response.StatusCode < 400 {
			t.Errorf("spec %q: %d", bad, response.StatusCode)
		}
	}
}

func TestTheEventStreamHonoursAfterAndAgent(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"one"}`).Body.Close()
	r.asAgent("reviewer", "POST", "/v1/status", `{"text":"two"}`).Body.Close()
	r.asAgent("coder", "POST", "/v1/status", `{"text":"three"}`).Body.Close()
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(body(t, r.asOperator("GET", "/events?agent=coder&after=1", ""))), "\n") {
		var e Event
		json.Unmarshal([]byte(line), &e)
		texts = append(texts, e.Text)
	}
	if strings.Join(texts, ",") != "three" {
		t.Errorf("events = %v", texts)
	}
}
