// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egzo-ai/egzo/internal/access"
)

func (r *rig) putAccess(byAgent map[string]access.Access) {
	r.t.Helper()
	data, _ := json.Marshal(byAgent)
	response := r.asOperator("PUT", "/v1/access", string(data))
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		r.t.Fatalf("PUT /v1/access = %d", response.StatusCode)
	}
}

func (r *rig) fromEgzo(agent string) []Message {
	return r.srv.events.messagesOf("agent:"+agent, func(m Message) bool { return m.From == egzoSender })
}

// everFromEgzo is every notice egzo sent the agent, the closed ones too.
func (r *rig) everFromEgzo(agent string) []Message {
	return r.srv.events.selectMessages(func(m Message) bool { return m.To == "agent:"+agent && m.From == egzoSender })
}

func team(allow ...string) access.Access {
	return access.Access{Profile: "team", Allow: allow, OtherHosts: access.OtherHosts, Services: []access.Service{}, SSH: []string{}}
}

func TestAnAgentReadsItsOwnAccessAndIsToldOnlyWhenItChanges(t *testing.T) {
	r := newRig(t)
	response := r.asAgent("coder", "GET", "/v1/access", "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("before core gave it: %d", response.StatusCode)
	}

	r.putAccess(map[string]access.Access{"coder": team("example.com"), "reviewer": team("example.com"), "gone": team("x")})
	var got access.Access
	decode(t, r.asAgent("coder", "GET", "/v1/access", ""), &got)
	if !access.Equal(got, team("example.com")) {
		t.Errorf("coder reads %+v", got)
	}
	if len(r.fromEgzo("coder")) != 0 {
		t.Error("the first access is given, not announced")
	}

	r.putAccess(map[string]access.Access{"coder": team("example.com", "pypi.org"), "reviewer": team("example.com")})
	told := r.fromEgzo("coder")
	if len(told) != 1 || told[0].Kind != kindRequest || !strings.Contains(told[0].Text, "pypi.org") {
		t.Fatalf("coder was told %+v", told)
	}
	if len(r.fromEgzo("reviewer")) != 0 {
		t.Error("an agent whose access is the same is not told")
	}
	r.putAccess(map[string]access.Access{"coder": team("example.com", "pypi.org")})
	if len(r.fromEgzo("coder")) != 1 {
		t.Error("the same access again tells nothing")
	}

	r.idle("coder")
	if line := r.claim("coder", time.Minute).Line; !strings.Contains(line, "your network access changed") || !strings.Contains(line, told[0].ID) {
		t.Errorf("the typed line: %q", line)
	}
}

func TestNobodyCanSendAsOrToEgzoAndARemovedAgentForgetsItsAccess(t *testing.T) {
	r := newRig(t)
	response := r.asAgent("coder", "POST", "/v1/messages", `{"to":"egzo","text":"hi"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("an agent messaging egzo: %d", response.StatusCode)
	}
	response = r.asOperator("POST", "/v1/messages", `{"from":"egzo","to":"agent:coder","text":"hi"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("the operator API sending as egzo: %d", response.StatusCode)
	}

	r.putAccess(map[string]access.Access{"coder": team("example.com")})
	response = r.asOperator("DELETE", "/v1/agents/coder", "")
	response.Body.Close()
	r.register("coder")
	r.putAccess(map[string]access.Access{"coder": team("pypi.org")})
	if len(r.fromEgzo("coder")) != 0 {
		t.Error("a new agent of the name starts with no access, so it is not told of a change")
	}
}

func TestTheAccessToolAnswersWhatTheAgentAPIDoes(t *testing.T) {
	r := newRig(t)
	r.putAccess(map[string]access.Access{"coder": team("example.com")})
	session, err := r.mcpSession("coder", r.token("coder"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result := call(t, session, "access", map[string]any{})
	if result.IsError {
		t.Fatalf("access: %+v", result.Content)
	}
	var got access.Access
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got); err != nil || !access.Equal(got, team("example.com")) {
		t.Errorf("access = %v (%v)", got, err)
	}
}

func TestANewNoticeReplacesTheOneTheAgentHasNotResolved(t *testing.T) {
	r := newRig(t)
	r.putAccess(map[string]access.Access{"coder": team("example.com")})
	r.putAccess(map[string]access.Access{"coder": team("example.com", "pypi.org")})
	r.putAccess(map[string]access.Access{"coder": team("example.com", "pypi.org", "npmjs.org")})
	told := r.everFromEgzo("coder")
	if len(told) != 2 || told[0].State != stateResolved || told[0].Outcome != outcomeSuperseded {
		t.Fatalf("the first notice is closed by the second: %+v", told)
	}
	if told[1].State == stateResolved || !strings.Contains(told[1].Text, "pypi.org") || !strings.Contains(told[1].Text, "npmjs.org") {
		t.Errorf("the open notice says all that changed since the access the agent knew: %+v", told[1])
	}

	r.putAccess(map[string]access.Access{"coder": team("example.com")})
	told = r.everFromEgzo("coder")
	if len(told) != 2 || told[1].State != stateResolved {
		t.Errorf("back to the access the agent knew: the open notice is closed and nothing is sent: %+v", told)
	}

	// once the agent is caught up, the next change is told against the access it has now
	r.putAccess(map[string]access.Access{"coder": team("example.com", "go.dev")})
	told = r.everFromEgzo("coder")
	if len(told) != 3 || strings.Contains(told[2].Text, "pypi.org") || !strings.Contains(told[2].Text, "go.dev") {
		t.Errorf("a new notice after the agent is caught up: %+v", told)
	}
}
