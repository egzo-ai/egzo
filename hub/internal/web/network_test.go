package web_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/internal/proxy"
)

func TestTheNetworkOfAProjectHasItsDenialsGroupedAndItsLatestEventsAndTheAgentsCountTheirs(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	a := agent("coder-1", "operator")
	a.ID = "01JBX7Q0000000000000000000"
	r.fakes["web"].SetAgent(a)
	start := time.Now().UTC()
	event := func(n int, e links.AuditEvent) links.AuditEvent {
		e.Time, e.Conn, e.Agent, e.Instance, e.Profile, e.Protocol = start.Add(time.Duration(n)*time.Millisecond), "00000000000000c1", "coder-1", a.ID, "default", proxy.HTTPS
		e.ClaimedAgent, e.Client, e.UserAgent = "coder-1", "10.89.3.2", "curl/8.11.1"
		return e
	}
	r.fakes["web"].AddAudit(
		event(1, links.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeDeny, Host: "registry.npmjs.org",
			Reason: &proxy.Reason{Code: proxy.CodeNotInProfile, Text: "not allowed by the agent's egress profile"}}),
		event(2, links.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeAllow, Mode: proxy.ModeInject, Host: "example.com", Service: "api"}),
		event(3, links.AuditEvent{Kind: proxy.KindRequest, Decision: proxy.OutcomeAllow, Host: "example.com", Method: "GET", Path: "/a/b", RawPath: "/a%2Fb",
			Status: 200, Injected: "api", Substituted: []string{"login"}}),
	)
	b := r.browser()
	b.signIn("admin")
	var page json_
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, page = b.get("/p/" + r.id("web") + "/network"); len(list(page, "denials")) == 1 && len(list(page, "events")) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if at(page, "feed", "state") != "live" {
		t.Fatalf("feed = %v", page["feed"])
	}
	denials := list(page, "denials")
	if len(denials) != 1 {
		t.Fatalf("denials = %v", denials)
	}
	d := denials[0]
	if d["host"] != "registry.npmjs.org" || d["code"] != "not-in-profile" || d["profile"] != "default" || d["agent"] != "coder-1" ||
		d["count"] != float64(1) || d["fixed"] != nil || d["creator"] == "" || d["claimedAgent"] != "coder-1" || d["client"] != "10.89.3.2" ||
		d["userAgent"] != "curl/8.11.1" {
		t.Errorf("denial = %v", d)
	}
	events := list(page, "events")
	if events[0]["event"] != "request" || events[0]["path"] != "/a/b" || events[0]["status"] != float64(200) || events[2]["decision"] != "deny" {
		t.Errorf("events, newest first = %v", events)
	}
	if e := events[0]; e["injected"] != "api" || e["rawPath"] != "/a%2Fb" || fmt.Sprint(e["substituted"]) != "[login]" || events[1]["service"] != "api" {
		t.Errorf("what the proxy did with the credentials = %v, %v", e, events[1])
	}
	if reason := events[2]["reason"].(map[string]any); reason["code"] != "not-in-profile" {
		t.Errorf("reason = %v", reason)
	}
	if e := events[0]; e["claimedAgent"] != "coder-1" || e["client"] != "10.89.3.2" || e["userAgent"] != "curl/8.11.1" {
		t.Errorf("who and from where = %v", e)
	}
	_, one := b.get("/p/" + r.id("web") + "/network?agent=coder-1")
	if len(list(one, "denials")) != 1 {
		t.Errorf("one agent's = %v", one)
	}
	if code, _ := b.get("/p/" + r.id("web") + "/network?agent=nobody"); code != 404 {
		t.Errorf("an agent that does not exist: %d", code)
	}
	_, p := b.get("/p/" + r.id("web"))
	if got := list(p, "agents")[0]["denied"]; got != float64(1) {
		t.Errorf("the agent's denied hosts = %v", got)
	}

	other := r.browser()
	other.signIn("mallory")
	if code, _ := other.get("/p/" + r.id("web") + "/network"); code != 404 {
		t.Errorf("a user with no role: %d", code)
	}
}
