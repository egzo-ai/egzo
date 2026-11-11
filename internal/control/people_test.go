// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (r *rig) put(path, body string) int {
	r.t.Helper()
	response := r.asOperator("PUT", path, body)
	response.Body.Close()
	return response.StatusCode
}

func (r *rig) startedBy(agent string) Self {
	r.t.Helper()
	session, err := r.mcpSession(agent, r.token(agent))
	if err != nil {
		r.t.Fatal(err)
	}
	defer session.Close()
	result := call(r.t, session, "agents", map[string]any{})
	var got agentsResult
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got); err != nil {
		r.t.Fatal(err)
	}
	return got.You
}

func (r *rig) starterNotices(agent string) []Message {
	var out []Message
	for _, m := range r.everFromEgzo(agent) {
		if m.Notice == noticeStarter {
			out = append(out, m)
		}
	}
	return out
}

func TestAnAgentIsToldWhenWhoStartedItCanAndCannotBeReachedAndNothingElse(t *testing.T) {
	r := newRig(t)
	r.put("/v1/identities", `{"coder":{"template":"t","harness":"h","starter":"user:7","starter_name":"Joe"},"reviewer":{"template":"t","harness":"h","starter":"user:8"}}`)
	if you := r.startedBy("coder"); you.StartedBy == nil || *you.StartedBy != (StartedBy{ID: "user:7", Name: "Joe"}) {
		t.Errorf("before the hub says = %+v", you.StartedBy)
	}
	people := func(reachable7, reachable8 bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]Person{"user:7": {Name: "Joe", Reachable: reachable7}, "user:8": {Name: "Ann", Reachable: reachable8}})
		if code := r.put("/v1/people", string(body)); code != http.StatusNoContent {
			t.Fatalf("people: %d", code)
		}
	}
	people(true, true)
	people(true, true)
	if told := r.starterNotices("coder"); len(told) != 0 {
		t.Errorf("the first word, and the same again, are no news: %+v", told)
	}
	if you := r.startedBy("coder"); you.StartedBy.Reachable == nil || !*you.StartedBy.Reachable {
		t.Errorf("after the hub says = %+v", you.StartedBy)
	}

	people(false, true)
	told := r.starterNotices("coder")
	if len(told) != 1 || !strings.Contains(told[0].Text, "Joe (user:7)") || !strings.Contains(told[0].Text, "can't be reached") ||
		!strings.Contains(told[0].Text, "set your status") || told[0].Kind != kindRequest {
		t.Errorf("told %+v", told)
	}
	if n := len(r.starterNotices("reviewer")); n != 0 {
		t.Errorf("an agent someone else started was told %d times", n)
	}
	people(false, true)
	people(true, true)
	told = r.starterNotices("coder")
	if len(told) != 2 || !strings.Contains(told[1].Text, "can be reached again") {
		t.Fatalf("told %+v", told)
	}
	if told[0].State != stateResolved || told[0].Outcome != outcomeSuperseded {
		t.Errorf("the notice it had not read = %s %s, want superseded", told[0].State, told[0].Outcome)
	}

	for _, bad := range []string{`{"operator":{"name":"x","reachable":true}}`, `{"agent:coder":{}}`, `{"user:7":{"name":"two\nlines"}}`, `[]`} {
		if code := r.put("/v1/people", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}

	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.startedBy("coder"); got == nil || got.Reachable == nil || !*got.Reachable || got.Name != "Joe" {
		t.Errorf("after a restart = %+v", got)
	}
}

func TestUpKeepsTheStartersNameSpawnGave(t *testing.T) {
	r := newRig(t)
	r.put("/v1/identities", `{"coder":{"template":"t","harness":"h","starter":"user:7","starter_name":"Joe"}}`)
	r.put("/v1/identities", `{"coder":{"template":"t","harness":"h","starter":"user:7"}}`)
	if got := r.srv.startedBy("coder"); got == nil || got.Name != "Joe" {
		t.Errorf("after up = %+v", got)
	}
	if code := r.put("/v1/identities", `{"coder":{"template":"t","harness":"h","starter":"nobody"}}`); code != http.StatusBadRequest {
		t.Errorf("a starter that is not one: %d", code)
	}
	r.put("/v1/identities", `{"reviewer":{"template":"t","harness":"h","starter":"operator"}}`)
	if got := r.startedBy("reviewer"); got.StartedBy == nil || *got.StartedBy != (StartedBy{ID: "operator"}) || got.Template != "t" {
		t.Errorf("an agent the operator started = %+v", got)
	}
}

func TestATaskIsTypedOnceAtTheFirstIdleBeforeAnyAnnouncement(t *testing.T) {
	r := newRig(t)
	if code := r.put("/v1/agents/coder/task", `{"text":"list the open issues"}`); code != http.StatusNoContent {
		t.Fatalf("task: %d", code)
	}
	if code := r.put("/v1/agents/nobody/task", `{"text":"x"}`); code != http.StatusNotFound {
		t.Errorf("a task for no agent: %d", code)
	}
	if code := r.put("/v1/agents/reviewer/task", `{"text":"  "}`); code != http.StatusBadRequest {
		t.Errorf("an empty task: %d", code)
	}
	r.request("coder", "a request")
	if got, _ := r.srv.claim("coder", 0); got.Line != "" {
		t.Errorf("typed %q before the agent was idle", got.Line)
	}
	r.srv.setActivity("coder", activityIdle)
	if got, _ := r.srv.claim("coder", 0); got.Line != "list the open issues" || len(got.IDs) != 0 {
		t.Errorf("the first claim = %+v, want the task", got)
	}
	if got, _ := r.srv.claim("coder", 0); got.Line == "list the open issues" || len(got.IDs) != 1 {
		t.Errorf("the second claim = %+v, want the request announced", got)
	}
	for _, e := range r.srv.events.all() {
		if strings.Contains(e.Text, "list the open issues") || strings.Contains(string(e.Data), "list the open issues") {
			t.Errorf("the task is in the event log: %+v", e)
		}
	}
}
