package hub

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// richPublished is what a link pushes for a project with one template whose harness has an AHP mode (oc) and one without (coder).
func richPublished() links.Published {
	return links.Published{Templates: map[string]string{"coder": "h1", "oc": "h2"}, AHP: map[string]links.AHPMode{"oc": {Terminal: true}}}
}

func TestTheProjectSaysWhichTemplatesHaveTheRichUI(t *testing.T) {
	p := withTemplates(links.Project{Name: "web"}, richPublished())
	if len(p.AHP) != 1 || !p.AHP["oc"].Terminal {
		t.Fatalf("AHP = %+v", p.AHP)
	}
	if p := withTemplates(links.Project{Name: "web"}, links.Published{Templates: map[string]string{"x": "h"}, AHP: map[string]links.AHPMode{"gone": {}}}); p.AHP != nil {
		t.Errorf("a template that is not published has no Rich UI: %+v", p.AHP)
	}
}

func TestARichUISpawnIsForATemplateThatHasOne(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder", "oc")
	web.SetPublished(richPublished())
	op := r.operator()
	eventually(t, "the templates to arrive", func() bool { p, _ := r.hub.knownProject(r.id("web")); return p.AHP != nil })
	if _, err := r.hub.StartSpawn(ctx, op, r.id("web"), Spawning{Template: "coder", Name: "plain", RichUI: true}); !errors.Is(err, ErrNoRichUI) {
		t.Errorf("a Rich UI spawn of a template without one: %v", err)
	}
	started, err := r.hub.StartSpawn(ctx, op, r.id("web"), Spawning{Template: "oc", Name: "rich", RichUI: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.hub.waitTask(ctx, started.ID); err != nil {
		t.Fatal(err)
	}
	if got := web.Spawns(); len(got) != 1 || !got[0].AHP || got[0].Name != "rich" {
		t.Errorf("spawns = %+v", got)
	}
}

func TestTheChatIsWatchedWithInspectAndActedInWithOperate(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "oc")
	op := r.operator()
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), carol.ID, rbac.Overseer)
	rich := agent("oc-1", rbac.Actor(alice.ID))
	rich.AHP = true
	web.SetAgent(rich)
	web.SetAgent(agent("plain-1", rbac.Actor(alice.ID)))

	if c, err := r.hub.OpenChat(ctx, alice, r.id("web"), "oc-1"); err != nil || c.ReadOnly {
		t.Errorf("the agent's starter acts in its chat: %+v %v", c, err)
	}
	if c, err := r.hub.OpenChat(ctx, op, r.id("web"), "oc-1"); err != nil || c.ReadOnly {
		t.Errorf("an operator acts in it: %+v %v", c, err)
	}
	if c, err := r.hub.OpenChat(ctx, bob, r.id("web"), "oc-1"); err != nil || !c.ReadOnly {
		t.Errorf("another user only watches: %+v %v", c, err)
	}
	if _, err := r.hub.OpenChat(ctx, carol, r.id("web"), "oc-1"); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
		t.Errorf("an overseer sees the task, not the chat: %v", err)
	}
	if _, err := r.hub.OpenChat(ctx, alice, r.id("web"), "plain-1"); !errors.Is(err, ErrNotAHP) {
		t.Errorf("an agent without the Rich UI: %v", err)
	}
	if _, err := r.hub.OpenChat(ctx, alice, r.id("web"), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an agent that is not there: %v", err)
	}
}

func TestTheChatCarriesLinesAndTheLinkHearsWhetherItIsReadOnly(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "oc")
	alice, bob := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)
	rich := agent("oc-1", rbac.Actor(alice.ID))
	rich.AHP = true
	web.SetAgent(rich)
	seen := make(chan bool, 2)
	web.ServeAHP("oc-1", func(conn net.Conn, readOnly bool) {
		defer conn.Close()
		seen <- readOnly
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Write([]byte(strings.Replace(line, "ping", "pong", 1)))
	})
	for _, who := range []struct {
		name     string
		readOnly bool
	}{{"alice", false}, {"bob", true}} {
		user := alice
		if who.name == "bob" {
			user = bob
		}
		c, err := r.hub.OpenChat(ctx, user, r.id("web"), "oc-1")
		if err != nil {
			t.Fatal(err)
		}
		var got string
		err = c.Run(ctx, func(conn net.Conn) error {
			conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
			got, _ = bufio.NewReader(conn).ReadString('\n')
			return nil
		})
		if err != nil || !strings.Contains(got, "pong") {
			t.Errorf("%s: %q %v", who.name, got, err)
		}
		if ro := <-seen; ro != who.readOnly {
			t.Errorf("%s: the link heard read-only=%v", who.name, ro)
		}
	}
}

func TestTheHubKeepsEveryMessageAndShowsAnAgentsToWhoeverMayWatchIt(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "oc")
	id := r.id("web")
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	r.store.SetRole(ctx, id, alice.ID, rbac.User)
	r.store.SetRole(ctx, id, bob.ID, rbac.User)
	r.store.SetRole(ctx, id, carol.ID, rbac.Overseer)
	rich := agent("oc-1", rbac.Actor(alice.ID))
	rich.AHP = true
	web.SetAgent(rich)
	web.SetAgent(agent("oc-2", rbac.Actor(alice.ID)))
	at := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	for i, e := range []links.Event{
		{Type: "message", ID: "m1", Agent: "oc-1", Actor: "user:" + alice.ID, To: "agent:oc-1", Kind: "request", Subject: "Login page", Text: "Fix the login page"},
		{Type: "message", ID: "m2", Agent: "oc-1", Actor: "agent:oc-1", To: "user:" + alice.ID, Kind: "update", Subject: "Re: Login page", Text: "Done", Re: "m1"},
		{Type: "message", ID: "m3", Agent: "oc-2", Actor: "user:" + alice.ID, To: "agent:oc-2", Kind: "request", Subject: "Other", Text: "Not this agent's"},
		{Type: "message", ID: "m4", Agent: "oc-1", Actor: "agent:oc-2", To: "agent:oc-1", Kind: "question", Re: "m1", Subject: "Which page?", Text: "Which **page**?"},
		{Type: "message", ID: "m5", Agent: "oc-2", Actor: "agent:oc-1", To: "agent:oc-2", Kind: "resolution", Re: "m4", Subject: "Re: Which page?", Text: "The login one", Outcome: "done"},
		{Type: "resolved", ID: "m4", Agent: "oc-1", Actor: "agent:oc-1", Outcome: "done"},
		{Type: "message", ID: "m6", Agent: "oc-1", Actor: "operator", To: "agent:oc-1", Kind: "request", Subject: "From the CLI", Text: "check the logs"},
	} {
		e.Seq, e.Time = i+1, at.Add(time.Duration(i)*time.Minute)
		if err := r.hub.handleEvent(ctx, id, e); err != nil {
			t.Fatal(err)
		}
	}
	// read again after a restart: nothing changes
	if err := r.hub.handleEvent(ctx, id, links.Event{Seq: 4, Type: "message", ID: "m4", Agent: "oc-1", Actor: "agent:oc-2", To: "agent:oc-1", Kind: "question", Text: "changed"}); err != nil {
		t.Fatal(err)
	}

	for _, who := range []store.User{alice, bob} {
		m, err := r.hub.ChatMessageOf(ctx, who, id, "oc-1", "m1")
		if err != nil || m.Text != "Fix the login page" || m.Subject != "Login page" || m.From != "alice" || m.FromAgent || m.Kind != "request" {
			t.Errorf("%s: %+v %v", who.Username, m, err)
		}
	}
	m, err := r.hub.ChatMessageOf(ctx, bob, id, "oc-1", "m4")
	if err != nil || m.From != "oc-2" || !m.FromAgent || m.Subject != "Which page?" || m.Text != "Which **page**?" || m.Re != "m1" || !m.Resolved || m.Outcome != "done" {
		t.Errorf("a message from another agent: %+v %v", m, err)
	}
	if m, err := r.hub.ChatMessageOf(ctx, bob, id, "oc-1", "m6"); err != nil || m.From != "operator" || m.FromAgent {
		t.Errorf("a message from the operator: %+v %v", m, err)
	}
	if out, _ := r.store.OutboxAfter(ctx, 0, 100); len(out) != 2 { // m1 and m3: alice links no chat account, so m2 reaches no bridge
		t.Errorf("the bridges read only what people and agents said to each other: %d entries", len(out))
	}
	for _, c := range []struct{ agent, msg, why string }{
		{"oc-1", "m2", "what the agent said is not a message it was sent"},
		{"oc-1", "m3", "a message to another agent"},
		{"oc-2", "m1", "a message to another agent, asked through that one"},
		{"oc-1", "m9", "a message the hub never had"},
	} {
		if _, err := r.hub.ChatMessageOf(ctx, alice, id, c.agent, c.msg); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", c.why, err)
		}
	}

	// its activity: what it sent and was sent, newest first
	list, err := r.hub.AgentMessages(ctx, bob, id, "oc-1", time.Time{}, 100)
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if err != nil || strings.Join(ids, " ") != "m6 m5 m4 m2 m1" {
		t.Errorf("oc-1's messages: %v %v", ids, err)
	}
	if list[3].To != "alice" || list[3].ToAgent || list[2].To != "oc-1" || !list[2].ToAgent {
		t.Errorf("who each went to, in words: %+v", list)
	}
	if older, _ := r.hub.AgentMessages(ctx, bob, id, "oc-1", list[2].Time, 100); len(older) != 2 || older[0].ID != "m2" {
		t.Errorf("paging back: %+v", older)
	}
	for _, who := range []store.User{carol} {
		if _, err := r.hub.AgentMessages(ctx, who, id, "oc-1", time.Time{}, 100); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			t.Errorf("an overseer does not see the agent's messages: %v", err)
		}
		if _, err := r.hub.ChatMessageOf(ctx, who, id, "oc-1", "m1"); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			t.Errorf("nor its chat's: %v", err)
		}
	}
}

func TestASubjectFromALinkIsOneLineOfPlainText(t *testing.T) {
	e := cleanEvent(links.Event{Type: "message", ID: "m1", Subject: "a \x1b]0;title\x07 subject\nsecond line " + strings.Repeat("x", 1000), Text: "a\nb"})
	if strings.ContainsAny(e.Subject, "\x1b\x07\n") || len(e.Subject) > maxSubject {
		t.Errorf("subject %q", e.Subject)
	}
	if e.Text != "a\nb" {
		t.Errorf("the text keeps its lines: %q", e.Text)
	}
	if cleanEvent(links.Event{Type: "status", Subject: "x"}).Subject != "" {
		t.Error("only a message has a subject")
	}
}
