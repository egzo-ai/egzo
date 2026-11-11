// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"net/http"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/access"
)

func (r *rig) undelivered(id, reason string) int {
	r.t.Helper()
	response := r.asOperator("POST", "/v1/messages/"+id+"/undelivered", `{"reason": "`+reason+`"}`)
	response.Body.Close()
	return response.StatusCode
}

// fromUser is a request a hub user sent coder, fetched.
func (r *rig) fromUser(text string) string {
	r.t.Helper()
	m, _, err := r.srv.send(sendRequest{From: "user:7", To: "agent:coder", Text: text})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.srv.fetch("coder", m.ID); err != nil {
		r.t.Fatal(err)
	}
	return m.ID
}

func TestAQuestionThatDidNotReachAPersonIsClosedAndTheAgentToldOnce(t *testing.T) {
	r := newRig(t)
	request := r.fromUser("deploy it?")
	question, err := r.srv.ask("coder", request, "to staging or production?", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code := r.undelivered(question, "user 7 cannot be reached on Discord."); code != http.StatusNoContent {
			t.Fatalf("undelivered = %d", code)
		}
	}
	told := r.everFromEgzo("coder")
	if len(told) != 1 || told[0].Notice != noticeUndelivered || told[0].Kind != kindRequest {
		t.Fatalf("one notice: %+v", told)
	}
	for _, said := range []string{question, "user:7", "cannot be reached on Discord.", `"to staging or production?"`, "closed it as failed", `re "` + question + `"`} {
		if !strings.Contains(told[0].Text, said) {
			t.Errorf("%q not in %q", said, told[0].Text)
		}
	}
	if strings.Contains(told[0].Text, "Discord..") {
		t.Errorf("the reason's own full stop is not doubled: %q", told[0].Text)
	}
	q, _ := r.srv.events.message(question)
	if q.State != stateResolved || q.Outcome != "failed" || waitingOn(r, "coder") {
		t.Errorf("the question is closed as failed, and coder waits on nothing: %+v", q)
	}

	// a change of access replaces the access notice, never this one
	r.putAccess(map[string]access.Access{"coder": team("example.com")})
	r.putAccess(map[string]access.Access{"coder": team("example.com", "pypi.org")})
	r.putAccess(map[string]access.Access{"coder": team("example.com", "go.dev")})
	if n, _ := r.srv.events.message(told[0].ID); n.State == stateResolved {
		t.Errorf("an access notice closed the undelivered one: %+v", n)
	}
}

func TestAResultThatDidNotReachAPersonStaysAndAnUpdateIsNotTold(t *testing.T) {
	r := newRig(t)
	request := r.fromUser("deploy it?")
	update, err := r.srv.update("coder", request, "building")
	if err != nil {
		t.Fatal(err)
	}
	if code := r.undelivered(update, "gone"); code != http.StatusNoContent || len(r.everFromEgzo("coder")) != 0 {
		t.Errorf("an update: %d, notices %+v", code, r.everFromEgzo("coder"))
	}
	result, err := r.srv.resolve("coder", request, "deployed", "done")
	if err != nil {
		t.Fatal(err)
	}
	if code := r.undelivered(result, "gone"); code != http.StatusNoContent {
		t.Fatalf("undelivered = %d", code)
	}
	told := r.everFromEgzo("coder")
	if len(told) != 1 || !strings.Contains(told[0].Text, "Your result "+result) || !strings.Contains(told[0].Text, "stays resolved") {
		t.Errorf("notices %+v", told)
	}
	if m, _ := r.srv.events.message(request); m.Outcome != "done" {
		t.Errorf("the request stays as coder closed it: %+v", m)
	}
}

func TestOnlyAnAgentsMessageToAUserWithAReasonCanBeUndelivered(t *testing.T) {
	r := newRig(t)
	request := r.fromUser("deploy it?")
	toOperator, _, err := r.srv.send(sendRequest{From: "agent:coder", To: "operator", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	question, _ := r.srv.ask("coder", request, "sure?", nil)
	for _, c := range []struct {
		id, reason string
		want       int
	}{
		{request, "x", http.StatusBadRequest},
		{toOperator.ID, "x", http.StatusBadRequest},
		{"m" + strings.Repeat("0", 32), "x", http.StatusNotFound},
		{question, " ", http.StatusBadRequest},
	} {
		if code := r.undelivered(c.id, c.reason); code != c.want {
			t.Errorf("%s %q: %d, want %d", c.id, c.reason, code, c.want)
		}
	}
}

func waitingOn(r *rig, agent string) bool {
	_, waiting := r.srv.events.owed(agent)
	return waiting
}
