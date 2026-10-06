package control

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestAnAgentIsKnownOnlyOnceRegisteredAndRegisteringTwiceIsFine(t *testing.T) {
	r := newRig(t)
	if r.srv.knownAgent("issue-1") {
		t.Fatal("an agent nobody registered is known")
	}
	if response := r.asOperator("POST", "/messages", `{"to":"agent:issue-1","text":"x"}`); response.StatusCode != http.StatusNotFound {
		t.Errorf("sending to an unregistered agent: status %d, want 404", response.StatusCode)
	}
	for i := 0; i < 2; i++ {
		if response := r.asOperator("PUT", "/agents/issue-1", ""); response.StatusCode != http.StatusNoContent {
			t.Fatalf("register #%d: status %d", i+1, response.StatusCode)
		}
	}
	if !r.srv.knownAgent("issue-1") {
		t.Error("a registered agent is unknown")
	}
	r.request("issue-1", "go")
}

func TestOnlyPlainNamesCanBeRegisteredOrUnregistered(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"..", "a%2Fb", "a%20b", "%2e%2e", strings.Repeat("a", 129), ".hidden"} {
		for _, method := range []string{"PUT", "DELETE"} {
			if response := r.asOperator(method, "/agents/"+name, ""); response.StatusCode < 400 {
				t.Errorf("%s %q: status %d", method, name, response.StatusCode)
			}
		}
	}
	if got := r.srv.projectAgents(); strings.Join(got, ",") != "coder,reviewer" {
		t.Errorf("agents = %v", got)
	}
}

func TestRegisteringManyAgentsAtOnceLosesNone(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.asOperator("PUT", fmt.Sprintf("/agents/w-%d", i), "").Body.Close()
		}()
	}
	wg.Wait()
	if got := len(r.srv.projectAgents()); got != 42 { // coder and reviewer, and forty
		t.Errorf("%d agents registered, want 42", got)
	}
}

func TestUnregisteringClosesWhatWasAskedOfTheAgentAndForgetsItsStatus(t *testing.T) {
	r := newRig(t)
	id := r.request("coder", "do the thing")
	other := r.request("coder", "and this")
	if _, err := r.srv.fetch("coder", other); err != nil {
		t.Fatal(err)
	}
	if err := r.srv.reportStatus("coder", "busy"); err != nil {
		t.Fatal(err)
	}
	if response := r.asOperator("DELETE", "/agents/coder", ""); response.StatusCode != http.StatusNoContent {
		t.Fatalf("unregister: status %d", response.StatusCode)
	}
	if r.srv.knownAgent("coder") {
		t.Error("the agent is still known")
	}
	for _, mid := range []string{id, other} {
		m, ok := r.srv.events.message(mid)
		if !ok || m.State != stateResolved || m.Outcome != "failed" {
			t.Errorf("message %s after the agent was removed: %+v", mid, m)
		}
	}
	// The sender can read why.
	var open []Message
	decode(t, r.asOperator("GET", "/messages?all=1&kind=resolution", ""), &open)
	if len(open) != 2 || !strings.Contains(open[0].Text, "removed") {
		t.Errorf("resolutions = %+v", open)
	}
	if response := r.asOperator("POST", "/messages", `{"to":"agent:coder","text":"x"}`); response.StatusCode != http.StatusNotFound {
		t.Errorf("sending to a removed agent: status %d", response.StatusCode)
	}
	// A new agent with the same name starts clean.
	r.register("coder")
	for _, status := range r.srv.agentStatuses() {
		if status.Agent == "coder" && (status.Status != "" || status.Open != 0) {
			t.Errorf("a new agent inherited %+v", status)
		}
	}
	// Unregistering what is not registered is fine.
	if response := r.asOperator("DELETE", "/agents/ghost", ""); response.StatusCode != http.StatusNoContent {
		t.Errorf("unregister an unknown agent: status %d", response.StatusCode)
	}
}

func TestTheForgottenStatusStaysForgottenAfterARestart(t *testing.T) {
	r := newRig(t)
	if err := r.srv.reportStatus("coder", "busy"); err != nil {
		t.Fatal(err)
	}
	r.asOperator("DELETE", "/agents/coder", "").Body.Close()
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.mu.Lock()
	defer reopened.mu.Unlock()
	if status, ok := reopened.events.statuses()["coder"]; ok {
		t.Errorf("the status came back: %+v", status)
	}
}

func TestTemplatesAreStoredWholeAndRefusedWhenOversizedOrEmpty(t *testing.T) {
	r := newRig(t)
	if response := r.asOperator("GET", "/templates", ""); response.StatusCode != http.StatusNotFound {
		t.Errorf("templates before any were published: status %d", response.StatusCode)
	}
	if response := r.asOperator("PUT", "/templates", ""); response.StatusCode != http.StatusBadRequest {
		t.Errorf("empty templates: status %d", response.StatusCode)
	}
	if response := r.asOperator("PUT", "/templates", strings.Repeat("x", maxTemplates+1)); response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized templates: status %d", response.StatusCode)
	}
	if response := r.asOperator("PUT", "/templates", `{"one":1}`); response.StatusCode != http.StatusNoContent {
		t.Fatalf("put: status %d", response.StatusCode)
	}
	r.asOperator("PUT", "/templates", `{"two":2}`).Body.Close()
	if got := body(t, r.asOperator("GET", "/templates", "")); got != `{"two":2}` {
		t.Errorf("templates = %q", got)
	}
	// they survive a restart: they are a file on the volume
	reopened, _ := newServer(r.srv.dir)
	recorder := newRecorder()
	reopened.handler().ServeHTTP(recorder, mustRequest("GET", "/templates", nil))
	if recorder.Body.String() != `{"two":2}` {
		t.Errorf("after a restart: %q", recorder.Body.String())
	}
}

func TestWhatAnAgentAskedAndWhatWasOnItsWayToItDiesWithIt(t *testing.T) {
	r := newRig(t)
	// coder is asked something, fetches it, and asks the operator a question of its own
	id := r.request("coder", "do the thing")
	if _, err := r.srv.fetch("coder", id); err != nil {
		t.Fatal(err)
	}
	question, err := r.srv.ask("coder", id, "which one?", nil)
	if err != nil {
		t.Fatal(err)
	}
	// reviewer replies to coder: a resolution that coder has not read
	reviewerRequest, err := r.srv.sendFromAgent("coder", "agent:reviewer", "look at this", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.fetch("reviewer", reviewerRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.resolve("reviewer", reviewerRequest, "looked", "done"); err != nil {
		t.Fatal(err)
	}
	if _, waiting := r.srv.overlay("coder"); !waiting {
		t.Fatal("setup: the question is not waiting")
	}

	r.asOperator("DELETE", "/agents/coder", "").Body.Close()
	r.register("coder")

	if _, waiting := r.srv.overlay("coder"); waiting {
		t.Error("a new coder is still waiting for an answer to the old coder's question")
	}
	if m, _ := r.srv.events.message(question); m.State != stateResolved {
		t.Errorf("the old question is still open: %+v", m)
	}
	if listed := r.srv.listFor("coder"); len(listed) != 0 {
		t.Errorf("a new coder inherited %d items: %+v", len(listed), listed)
	}
}

func TestAnAgentThatIsNotRegisteredCannotReportEvenWithAValidToken(t *testing.T) {
	r := newRig(t)
	token := r.token("ghost") // derived from the project key: a valid token for a name nobody registered
	if response := r.asAgent("ghost", "POST", "/v1/status", `{"text":"hi"}`); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unregistered agent was served: status %d (token %s...)", response.StatusCode, token[:6])
	}
	r.register("ghost")
	if response := r.asAgent("ghost", "POST", "/v1/status", `{"text":"hi"}`); response.StatusCode >= 300 {
		t.Errorf("a registered agent was refused: status %d", response.StatusCode)
	}
	r.asOperator("DELETE", "/agents/ghost", "").Body.Close()
	if response := r.asAgent("ghost", "POST", "/v1/status", `{"text":"again"}`); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("a removed agent still reports: status %d", response.StatusCode)
	}
	for _, status := range r.srv.agentStatuses() {
		if status.Agent == "ghost" {
			t.Errorf("a removed agent is listed: %+v", status)
		}
	}
}

func TestASendAndAnUnregisterAtTheSameTimeLeaveNothingOpenForTheRemovedAgent(t *testing.T) {
	for i := 0; i < 25; i++ {
		r := newRig(t)
		r.register("racer")
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.asOperator("POST", "/messages", `{"to":"agent:racer","text":"x"}`).Body.Close()
		}()
		r.asOperator("DELETE", "/agents/racer", "").Body.Close()
		<-done
		for _, m := range r.srv.events.messagesOf("agent:racer", func(m Message) bool { return m.To == "agent:racer" && m.State != stateResolved }) {
			t.Fatalf("round %d: a message stays open for the removed agent: %+v", i, m)
		}
	}
}

func TestACompactedLogReplaysARetirementBetweenTwoStatuses(t *testing.T) {
	r := newRig(t)
	r.srv.reportStatus("coder", "before")
	r.asOperator("DELETE", "/agents/coder", "").Body.Close()
	r.register("coder")
	r.srv.reportStatus("coder", "after")
	r.srv.events.mu.Lock()
	err := r.srv.events.compact()
	r.srv.events.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if status := reopened.events.statuses()["coder"]; status.Status != "after" {
		t.Errorf("after compaction and a restart the status is %+v, want the new coder's", status)
	}
}
