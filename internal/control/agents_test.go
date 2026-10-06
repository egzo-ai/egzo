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
