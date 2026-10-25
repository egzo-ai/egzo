// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestASpawnIsListedWhileItRunsGoesOnWithoutTheRequestAndSaysHowItEnded(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)
	r.fakes["local"].HoldSpawns()

	request, cancel := context.WithCancel(ctx)
	started, err := r.hub.StartSpawn(request, alice, "local", "web", "coder", "")
	if err != nil || started.ID == "" || started.State != SpawnPending {
		t.Fatalf("start: %+v %v", started, err)
	}
	cancel() // the browser, or a proxy in front of the hub, gave up on the request

	// everyone who sees the project sees the spawn, and who asked for it
	view, _ := r.hub.Project(ctx, bob, "local", "web")
	if len(view.Spawns) != 1 || view.Spawns[0].ID != started.ID || view.Spawns[0].State != SpawnPending ||
		view.Spawns[0].Template != "coder" || view.Spawns[0].Actor != rbac.Actor(alice.ID) {
		t.Fatalf("spawns while it runs = %+v", view.Spawns)
	}
	if len(view.Agents) != 0 {
		t.Errorf("an agent before the spawn ended: %+v", view.Agents)
	}

	r.fakes["local"].ReleaseSpawns()
	r.hub.WaitForSpawns()
	view, _ = r.hub.Project(ctx, alice, "local", "web")
	if len(view.Agents) != 1 || view.Agents[0].Name != "coder-1" || view.Agents[0].Actor != rbac.Actor(alice.ID) {
		t.Fatalf("the spawn did not go on once the request was gone: %+v", view.Agents)
	}
	if len(view.Spawns) != 1 || view.Spawns[0].State != SpawnReady || view.Spawns[0].Agent != "coder-1" || view.Spawns[0].Ended.IsZero() {
		t.Errorf("an ended spawn says how it ended: %+v", view.Spawns)
	}
}

func TestAFailedSpawnIsListedWithWhy(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	r.scan()
	started, err := r.hub.StartSpawn(ctx, op, "local", "web", "ghost", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	r.hub.WaitForSpawns()
	view, _ := r.hub.Project(ctx, op, "local", "web")
	if len(view.Spawns) != 1 || view.Spawns[0].ID != started.ID || view.Spawns[0].State != SpawnFailed || !strings.Contains(view.Spawns[0].Err, "ghost") {
		t.Errorf("a failed spawn = %+v", view.Spawns)
	}
	if _, err := r.hub.Spawn(ctx, op, "local", "web", "ghost", ""); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("waiting for a spawn that fails: %v", err)
	}
}

func TestASpawnTheUserMayNotDoIsRefusedAtOnceAndNotListed(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	alice := r.login("alice")
	r.scan()
	if _, err := r.hub.StartSpawn(ctx, alice, "local", "web", "coder", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a spawn without a role: %v", err)
	}
	if view, _ := r.hub.Project(ctx, op, "local", "web"); len(view.Spawns) != 0 {
		t.Errorf("a refused spawn is listed: %+v", view.Spawns)
	}
}

func TestTheSpawnsOfAProjectRunOneAfterTheOther(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	r.scan()
	r.fakes["local"].HoldSpawns()
	first, _ := r.hub.StartSpawn(ctx, op, "local", "web", "coder", "one")
	second, _ := r.hub.StartSpawn(ctx, op, "local", "web", "coder", "two")
	view, _ := r.hub.Project(ctx, op, "local", "web")
	if len(view.Spawns) != 2 || view.Spawns[0].ID != first.ID || view.Spawns[1].ID != second.ID {
		t.Fatalf("both are listed, oldest first: %+v", view.Spawns)
	}
	r.fakes["local"].ReleaseSpawns()
	r.hub.WaitForSpawns()
	calls := r.fakes["local"].Calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "spawn web/one ") || !strings.HasPrefix(calls[1], "spawn web/two ") {
		t.Errorf("the engine was asked in order, one at a time: %v", calls)
	}
}
