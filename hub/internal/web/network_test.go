package web_test

import (
	"context"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/internal/proxy"
)

func TestTheNetworkOfAProjectHasItsDenialsGroupedAndItsLatestEventsAndTheAgentsCountTheirs(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := agent("coder-1", "operator")
	a.ID = "01JBX7Q0000000000000000000"
	r.fakes["local"].SetAgent("web", a)
	feeds, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	r.hub.StartFeeds(feeds, hub.FeedOptions{Flush: 5 * time.Millisecond, BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	r.hub.Scan(ctx)
	start := time.Now().UTC()
	event := func(n int, e engines.AuditEvent) engines.AuditEvent {
		e.Time, e.Conn, e.Agent, e.Instance, e.Profile, e.Protocol = start.Add(time.Duration(n)*time.Millisecond), "00000000000000c1", "coder-1", a.ID, "default", proxy.HTTPS
		return e
	}
	r.fakes["local"].AddAudit("web",
		event(1, engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeDeny, Host: "registry.npmjs.org",
			Reason: &proxy.Reason{Code: proxy.CodeNotInProfile, Text: "not allowed by the agent's egress profile"}}),
		event(2, engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeAllow, Mode: proxy.ModeInspect, Host: "example.com"}),
		event(3, engines.AuditEvent{Kind: proxy.KindRequest, Decision: proxy.OutcomeAllow, Host: "example.com", Method: "GET", Path: "/", Status: 200}),
	)
	b := r.browser()
	b.signIn("admin")
	var page json_
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, page = b.get("/e/local/p/web/network"); len(list(page, "denials")) == 1 && len(list(page, "events")) == 3 {
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
		d["count"] != float64(1) || d["fixed"] != nil || d["creator"] == "" {
		t.Errorf("denial = %v", d)
	}
	events := list(page, "events")
	if events[0]["event"] != "request" || events[0]["path"] != "/" || events[0]["status"] != float64(200) || events[2]["decision"] != "deny" {
		t.Errorf("events, newest first = %v", events)
	}
	if reason := events[2]["reason"].(map[string]any); reason["code"] != "not-in-profile" {
		t.Errorf("reason = %v", reason)
	}
	_, one := b.get("/e/local/p/web/network?agent=coder-1")
	if len(list(one, "denials")) != 1 {
		t.Errorf("one agent's = %v", one)
	}
	if code, _ := b.get("/e/local/p/web/network?agent=nobody"); code != 404 {
		t.Errorf("an agent that does not exist: %d", code)
	}
	_, p := b.get("/e/local/p/web")
	if got := list(p, "agents")[0]["denied"]; got != float64(1) {
		t.Errorf("the agent's denied hosts = %v", got)
	}

	other := r.browser()
	other.signIn("mallory")
	if code, _ := other.get("/e/local/p/web/network"); code != 404 {
		t.Errorf("a user with no role: %d", code)
	}
}
