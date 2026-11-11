// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"errors"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestASpawnTellsTheAgentWhoStartedItAndAnUnattendedOneNeedsATaskAndAChatAccount(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	op := r.operator()
	alice := r.login("alice")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)

	if _, err := r.hub.StartSpawn(ctx, alice, r.id("web"), Spawning{Template: "coder", Name: "a", Unattended: true}); !errors.Is(err, ErrTaskRequired) {
		t.Errorf("unattended without a task: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, alice, r.id("web"), Spawning{Template: "coder", Name: "a", Task: "deploy", Unattended: true}); !errors.Is(err, ErrNoChatAccount) {
		t.Errorf("unattended without a chat account: %v", err)
	}
	long := make([]byte, maxTask+1)
	if _, err := r.hub.StartSpawn(ctx, alice, r.id("web"), Spawning{Template: "coder", Name: "a", Task: string(long)}); !errors.Is(err, ErrTaskTooLong) {
		t.Errorf("a task too long: %v", err)
	}
	if len(web.Spawns()) != 0 {
		t.Fatalf("a refused spawn reached the project: %+v", web.Spawns())
	}

	started, err := r.hub.StartSpawn(ctx, alice, r.id("web"), Spawning{Template: "coder", Name: "attended", Task: "look at issue 9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.hub.waitTask(ctx, started.ID); err != nil {
		t.Fatal(err)
	}
	r.account(alice)
	started, err = r.hub.StartSpawn(ctx, alice, r.id("web"), Spawning{Template: "coder", Name: "alone", Task: "deploy", Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.hub.waitTask(ctx, started.ID); err != nil {
		t.Fatal(err)
	}
	r.hub.Spawn(ctx, op, r.id("web"), "coder", "plain")
	want := []links.SpawnRequest{
		{Template: "coder", Name: "attended", Actor: rbac.Actor(alice.ID), StarterName: "alice", Task: "look at issue 9"},
		{Template: "coder", Name: "alone", Actor: rbac.Actor(alice.ID), StarterName: "alice", Task: "deploy", Unattended: true},
		{Template: "coder", Name: "plain", Actor: rbac.Actor(op.ID), StarterName: displayName(op)},
	}
	if got := web.Spawns(); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("spawns = %+v, want %+v", got, want)
	}
	if tasks, _ := r.store.OpenTasks(ctx); len(tasks) > 0 && tasks[0].Template == "look at issue 9" {
		t.Error("the hub kept the task's text")
	}
}

func TestAProjectIsToldWhetherWhoStartedItsAgentsCanBeReached(t *testing.T) {
	r := newRig(t)
	web := r.link("web", "coder")
	op := r.operator()
	alice := r.login("alice")
	if err := r.hub.SetRole(ctx, op, r.id("web"), alice.ID, rbac.User); err != nil {
		t.Fatal(err)
	}
	said := func() map[string]links.Person {
		all := web.PeopleSaid()
		if len(all) == 0 {
			return nil
		}
		return all[len(all)-1]
	}
	alicePerson := func(reachable bool) func() bool {
		return func() bool {
			p, ok := said()[rbac.Actor(alice.ID)]
			return ok && p.Name == "alice" && p.Reachable == reachable
		}
	}
	if err := r.hub.Spawn(ctx, alice, r.id("web"), "coder", "coder-1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the project to hear alice cannot be reached: no chat account", alicePerson(false))
	r.account(alice)
	eventually(t, "the project to hear alice can be reached", alicePerson(true))
	if _, err := r.hub.SetDisabled(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "disabled, she cannot be reached", alicePerson(false))
	if _, err := r.hub.SetDisabled(ctx, op, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "enabled, she can again", alicePerson(true))
	if err := r.hub.SetRole(ctx, op, r.id("web"), alice.ID, rbac.Viewer); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a viewer chats with no agent", alicePerson(false))

	before := len(web.PeopleSaid())
	r.hub.tellPeople(r.id("web"))
	r.hub.tellPeople(r.id("web"))
	if n := len(web.PeopleSaid()); n != before {
		t.Errorf("told %d times more when nothing changed", n-before)
	}
	if len(said()) != 1 {
		t.Errorf("the operator spawned nothing here, and alice is told of once: %+v", said())
	}
}
