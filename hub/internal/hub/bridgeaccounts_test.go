// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"errors"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func types(events []BridgeEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Type+" "+e.Agent+e.User)
	}
	return out
}

func TestAnAdminMayAddAnAgentToAPlaceAndOthersMayNot(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice, bob := r.login("alice"), r.login("bob")
	id := r.id("web")
	r.store.SetRole(ctx, id, alice.ID, rbac.Admin)
	r.store.SetRole(ctx, id, bob.ID, rbac.User)
	web.SetAgent(agent("coder-1", "user:"+bob.ID))
	a, b := r.account(alice), r.account(bob)
	for _, c := range []struct {
		account, agent string
		want           bool
	}{{a, "coder-1", true}, {b, "coder-1", false}, {"d-unlinked", "coder-1", false}, {a, "ghost-1", false}} {
		if got, err := r.hub.MayAdd(ctx, "discord", c.account, id, c.agent); err != nil || got != c.want {
			t.Errorf("%s adding %s: %v %v, want %v", c.account, c.agent, got, err, c.want)
		}
	}
}

func TestAPersonIsListedTheAgentsTheyMayChatWith(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice, bob := r.login("alice"), r.login("bob")
	id := r.id("web")
	r.store.SetRole(ctx, id, alice.ID, rbac.User)
	r.store.SetRole(ctx, id, bob.ID, rbac.Admin)
	web.SetAgent(agent("coder-1", "user:"+alice.ID))
	web.SetAgent(agent("cli-1", rbac.CLI))
	list, err := r.hub.ChatAgents(ctx, "discord", r.account(alice))
	if err != nil || len(list) != 1 || list[0].Agent != "coder-1" || list[0].ProjectName != "web" || list[0].Admin {
		t.Errorf("alice: %+v %v", list, err)
	}
	if list, _ := r.hub.ChatAgents(ctx, "discord", r.account(bob)); len(list) != 2 || !list[0].Admin {
		t.Errorf("an admin: %+v", list)
	}
	if _, err := r.hub.ChatAgents(ctx, "discord", "d-unlinked"); !errors.Is(err, ErrForbidden) {
		t.Errorf("an account nobody linked: %v", err)
	}
}

func TestBridgesHearOfWhatChangesWhoMayChat(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	alice := r.login("alice")
	id := r.id("web")
	web.SetAgent(agent("coder-1", "user:"+alice.ID))
	web.SetAgent(agent("coder-2", "user:"+alice.ID))
	_, start, _, _ := r.hub.Outbox(ctx, -1, 10)

	account := r.account(alice)
	if err := r.hub.SetRole(ctx, r.operator(), id, alice.ID, rbac.User); err != nil {
		t.Fatal(err)
	}
	web.RemoveAgent("coder-2")
	if err := r.hub.UnlinkBridgeAccount(ctx, alice, "discord", account); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.SetDisabled(ctx, r.operator(), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	events, _, _, _ := r.hub.Outbox(ctx, start, 20)
	want := []string{"account.linked " + alice.ID, "role.changed " + alice.ID, "agent.removed coder-2", "account.unlinked " + alice.ID, "user.disabled " + alice.ID}
	if got := types(events); len(got) != len(want) {
		t.Fatalf("heard %q, want %q", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("event %d: %q, want %q", i, got[i], want[i])
			}
		}
	}
	if events[2].Project != id || events[1].Project != id || events[0].Bridge != "discord" || events[0].Account != account {
		t.Errorf("events = %+v", events)
	}
}

func TestABridgeHearsWhetherAnAgentIsStillInItsProject(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	id := r.id("web")
	web.SetAgent(agent("coder-1", "operator"))
	web.SetAgent(agent("coder-2", "operator"))
	web.RemoveAgent("coder-2")
	presence := func(h *Hub) []string {
		got, err := h.AgentsPresent(ctx, []AgentRef{{id, "coder-1"}, {id, "coder-2"}, {"nosuchproject", "coder-1"}})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range got {
			out = append(out, p.Agent+" "+p.State)
		}
		return out
	}
	eventually(t, "coder-2 to be gone", func() bool {
		got := presence(r.hub)
		return got[0] == "coder-1 present" && got[1] == "coder-2 absent" && got[2] == "coder-1 absent"
	})

	// a hub that just started has heard nothing from the link yet: it cannot say
	restarted := New(r.store, nil, flushing)
	if got := presence(restarted); got[0] != "coder-1 unknown" || got[1] != "coder-2 unknown" || got[2] != "coder-1 absent" {
		t.Errorf("after a restart: %q", got)
	}

	// a revoked project never comes back under its id: its agents are gone, and the bridges hear so
	_, start, _, _ := r.hub.Outbox(ctx, -1, 10)
	if err := r.hub.Revoke(ctx, r.operator(), id, "stolen"); err != nil {
		t.Fatal(err)
	}
	if events, _, _, _ := r.hub.Outbox(ctx, start, 10); len(events) != 1 || events[0].Type != EventProjectRemoved || events[0].Project != id {
		t.Errorf("after a revoke, the bridges heard %+v", events)
	}
	if got := presence(r.hub); got[0] != "coder-1 absent" || got[1] != "coder-2 absent" {
		t.Errorf("after a revoke: %q", got)
	}
}
