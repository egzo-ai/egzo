// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestAUserChatsWithTheirOwnAgentsAndAnAdministratorWithAnyAndEveryRefusalIsAudited(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	id := r.id("web")
	r.store.SetRole(ctx, id, alice.ID, rbac.User)
	r.store.SetRole(ctx, id, bob.ID, rbac.User)
	r.store.SetRole(ctx, id, carol.ID, rbac.Admin)
	web.SetAgent(agent("coder-1", "user:"+alice.ID))
	web.SetAgent(agent("cli-1", rbac.CLI))

	say := func(who, to, key string) error {
		_, err := r.hub.Deliver(ctx, id, to, Inbound{Via: "discord", User: who, Text: "hello", Key: key})
		return err
	}
	if err := say(alice.ID, "coder-1", "k1"); err != nil {
		t.Errorf("alice to her own agent: %v", err)
	}
	if err := say(bob.ID, "coder-1", "k2"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob to alice's agent: %v", err)
	}
	if err := say(alice.ID, "cli-1", "k3"); !errors.Is(err, ErrForbidden) {
		t.Errorf("alice to an agent of the CLI: %v", err)
	}
	if err := say(carol.ID, "cli-1", "k4"); err != nil {
		t.Errorf("an administrator to any agent: %v", err)
	}
	if err := say("u-nobody", "coder-1", "k5"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user the hub does not know: %v", err)
	}
	if _, _, err := r.store.SetDisabled(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := say(alice.ID, "coder-1", "k6"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a disabled user: %v", err)
	}

	var sends []string
	for _, call := range web.Calls() {
		if strings.HasPrefix(call, "send ") {
			sends = append(sends, call)
		}
	}
	if want := []string{"send coder-1 user:" + alice.ID + " k1", "send cli-1 user:" + carol.ID + " k4"}; strings.Join(sends, ",") != strings.Join(want, ",") {
		t.Errorf("sent %v, want %v", sends, want)
	}
	entries, _ := r.store.AuditOf(ctx, id, 50)
	var allowed, refused int
	for _, e := range entries {
		if e.Action != "agent.message" {
			continue
		}
		if strings.Contains(e.Detail, "hello") {
			t.Errorf("the audit holds a message's text: %+v", e)
		}
		if !strings.Contains(e.Detail, "discord") {
			t.Errorf("the audit does not say which bridge: %+v", e)
		}
		if e.Allowed {
			allowed++
		} else {
			refused++
		}
	}
	if allowed != 2 || refused != 4 {
		t.Errorf("audited %d allowed and %d refused, want 2 and 4", allowed, refused)
	}
}

func TestAnAnswerGoesToTheProjectUnderTheSameRule(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice, bob := r.login("alice"), r.login("bob")
	id := r.id("web")
	r.store.SetRole(ctx, id, alice.ID, rbac.User)
	r.store.SetRole(ctx, id, bob.ID, rbac.User)
	web.SetAgent(agent("coder-1", "user:"+alice.ID))
	if _, err := r.hub.Deliver(ctx, id, "coder-1", Inbound{Via: "discord", User: bob.ID, Text: "yes", Answer: "mq"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob answering for alice's agent: %v", err)
	}
	if _, err := r.hub.Deliver(ctx, id, "coder-1", Inbound{Via: "discord", User: alice.ID, Text: "yes", Answer: "mq"}); err != nil {
		t.Errorf("alice: %v", err)
	}
	calls := web.Calls()
	if last := calls[len(calls)-1]; last != "resolve mq user:"+alice.ID+" yes" {
		t.Errorf("last call = %q", last)
	}
}

func TestAMessageForAnOfflineProjectIsDroppedAndAudited(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice := r.login("alice")
	id := r.id("web")
	r.store.SetRole(ctx, id, alice.ID, rbac.Admin)
	web.SetAgent(agent("coder-1", rbac.CLI))
	web.Drop()
	eventually(t, "the hub to see it go", func() bool { return r.hub.connected(id) == nil })
	if _, err := r.hub.Deliver(ctx, id, "coder-1", Inbound{Via: "discord", User: alice.ID, Text: "x", Key: "k"}); err == nil {
		t.Error("a message for an offline project went somewhere")
	}
}

func TestWhatAgentsAndUsersSayToEachOtherGoesInTheOutboxAndTheFeedResumesWhereTheHubIs(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	id := r.id("web")
	bob := r.login("bob")
	r.store.SetRole(ctx, id, bob.ID, rbac.Admin)
	carol := r.login("carol") // no role on the project
	_, _, wake, err := r.hub.Outbox(ctx, -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + bob.ID, Kind: "question", Text: "where?\nstaging or prod", ID: "m1", Choices: []string{"staging", "prod"}})
	select {
	case <-wake:
	default:
		t.Error("a bridge waiting for more was not woken")
	}
	web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "agent:reviewer-1", Kind: "request", ID: "m2"})
	web.PushEvent(links.Event{Type: "status", Agent: "coder-1", Text: "thinking"})
	web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + carol.ID, Kind: "request", Text: "hello stranger", ID: "m4"})
	web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "user:" + bob.ID, To: "agent:coder-1", Kind: "resolution", Text: "staging", Re: "m1", ID: "m3"})

	got, last, _, err := r.hub.Outbox(ctx, 0, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("outbox = %+v, %v", got, err)
	}
	q, a := got[0], got[1]
	if q.Project != id || q.Agent != "coder-1" || q.From != "agent:coder-1" || q.To != "user:"+bob.ID || q.Kind != "question" || q.Text != "where?\nstaging or prod" || len(q.Choices) != 2 {
		t.Errorf("the question = %+v", q)
	}
	if a.Kind != "resolution" || a.From != "user:"+bob.ID || a.Re != "m1" || a.Seq != last || a.Seq <= q.Seq {
		t.Errorf("the answer = %+v (last %d)", a, last)
	}
	if more, _, _, _ := r.hub.Outbox(ctx, last, 10); len(more) != 0 {
		t.Errorf("after the last: %+v", more)
	}
	if fromNow, at, _, _ := r.hub.Outbox(ctx, -1, 10); len(fromNow) != 0 || at != last {
		t.Errorf("from now: %+v at %d", fromNow, at)
	}
	if again, _, _, _ := r.hub.Outbox(ctx, last+100, 10); len(again) != 2 {
		t.Errorf("past the end is a database made anew: all of it, got %+v", again)
	}
	if seq, _ := r.store.EventCursor(ctx, id); seq != 5 {
		t.Errorf("cursor = %d, want 5", seq)
	}
	web.Drop()
	eventually(t, "the hub to see it go", func() bool { return r.hub.connected(id) == nil })
	r.connect(web)
	eventually(t, "the feed to open again", func() bool { return len(web.EventsAfters()) == 2 })
	if afters := web.EventsAfters(); afters[0] != -1 || afters[1] != 5 {
		t.Errorf("the events feed began after %v, want [-1 5]", afters)
	}
}

func TestAnEventThatWasNotKeptIsAnErrorAndTheCursorStays(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	id := r.id("web")
	alice := r.login("alice")
	r.store.SetRole(ctx, id, alice.ID, rbac.Admin)
	if err := r.hub.handleEvent(ctx, id, links.Event{Type: "start", Seq: 7}); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel() // the write fails
	event := links.Event{Seq: 8, Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + alice.ID, Kind: "request", Text: "hi", ID: "m1"}
	if err := r.hub.handleEvent(gone, id, event); err == nil {
		t.Error("a write that failed was not an error")
	}
	if seq, _ := r.store.EventCursor(ctx, id); seq != 7 {
		t.Errorf("cursor = %d, want 7", seq)
	}
}
