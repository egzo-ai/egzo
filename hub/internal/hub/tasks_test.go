// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// agentIn is the agent of that name in what the user sees of the project, and how many agents have it.
func (r *rig) agentIn(user store.User, project, name string) (AgentView, int) {
	r.t.Helper()
	view, err := r.hub.Project(ctx, user, project)
	if err != nil {
		r.t.Fatal(err)
	}
	var found AgentView
	n := 0
	for _, a := range view.Agents {
		if a.Name == name {
			found, n = a, n+1
		}
	}
	return found, n
}

func TestASpawnIsOneAgentFromWhenItIsAskedForAndGoesOnWithoutTheRequest(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)
	r.fakes["web"].Hold()

	request, cancel := context.WithCancel(ctx)
	started, err := r.hub.StartSpawn(request, alice, r.id("web"), "coder", "fix-bug")
	if err != nil || started.ID == "" || started.State != store.TaskRunning || started.Verb != VerbSpawn {
		t.Fatalf("start: %+v %v", started, err)
	}
	cancel() // the browser, or a proxy in front of the hub, gave up on the request

	// everyone who sees the project sees the agent being made, by whom, and the step it is at
	eventually(t, "the first step", func() bool {
		a, _ := r.agentIn(bob, r.id("web"), "fix-bug")
		return a.Task != nil && a.Task.Step == "create network web-fix-bug"
	})
	a, n := r.agentIn(bob, r.id("web"), "fix-bug")
	if n != 1 || a.State != "" || a.Template != "coder" || a.Actor != rbac.Actor(alice.ID) || a.Task.ID != started.ID ||
		a.Task.State != store.TaskRunning || a.Task.CanDismiss {
		t.Fatalf("the agent while it is made = %d %+v %+v", n, a, a.Task)
	}

	r.fakes["web"].Release()
	r.hub.WaitForTasks()
	a, n = r.agentIn(alice, r.id("web"), "fix-bug")
	if n != 1 || a.State != "running" || a.Actor != rbac.Actor(alice.ID) || a.Task != nil {
		t.Fatalf("the agent once made = %d %+v %+v", n, a, a.Task)
	}
	ended, _ := r.hub.TaskOf(ctx, alice, r.id("web"), started.ID)
	if ended.State != store.TaskDone || ended.Ended.IsZero() || ended.Step != "create container web-fix-bug-1" {
		t.Errorf("the task once ended = %+v", ended)
	}
}

func TestASpawnNeedsANameNotTakenARoleAndIsRefusedAtOnce(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("taken", "operator"))
	op := r.operator()
	alice := r.login("alice")
	if _, err := r.hub.StartSpawn(ctx, op, r.id("web"), "coder", ""); !errors.Is(err, ErrNameRequired) {
		t.Errorf("a spawn without a name: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, op, r.id("web"), "coder", "Not-Valid"); !errors.Is(err, ErrBadName) {
		t.Errorf("a spawn of a name an agent cannot have: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, op, r.id("web"), "coder", "taken"); !errors.As(err, new(ExistsError)) {
		t.Errorf("a spawn of a name an agent has: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, alice, r.id("web"), "coder", "mine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a spawn without a role: %v", err)
	}
	if tasks, _ := r.hub.Tasks(ctx, op, r.id("web"), "", 10); len(tasks) != 0 {
		t.Errorf("a refused spawn is a task: %+v", tasks)
	}
}

func TestAFailedSpawnStaysAnAgentWithWhyUntilItIsDismissed(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)

	if err := r.hub.Spawn(ctx, alice, r.id("web"), "ghost", "lost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("waiting for a spawn that fails: %v", err)
	}
	// bob sees it, and may not take it away: it is alice's, as the agent would have been
	a, n := r.agentIn(bob, r.id("web"), "lost")
	if n != 1 || a.Task == nil || a.Task.State != store.TaskFailed || !strings.Contains(a.Task.Err, "ghost") || a.Task.CanDismiss {
		t.Fatalf("bob sees %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, bob, r.id("web"), a.Task.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob dismissing alice's failure: %v", err)
	}
	if a, _ := r.agentIn(alice, r.id("web"), "lost"); !a.Task.CanDismiss {
		t.Fatal("alice cannot dismiss her failed spawn")
	}
	if err := r.hub.DismissTask(ctx, alice, r.id("web"), a.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, n := r.agentIn(bob, r.id("web"), "lost"); n != 0 {
		t.Error("a dismissed failed spawn is still an agent")
	}
	if err := r.hub.DismissTask(ctx, alice, r.id("web"), a.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("dismissing it again: %v", err)
	}
	// the activity keeps it
	if tasks, _ := r.hub.Tasks(ctx, bob, r.id("web"), "", 10); len(tasks) != 1 || tasks[0].State != store.TaskFailed || !tasks[0].Dismissed {
		t.Errorf("the activity = %+v", tasks)
	}
}

func TestAnOperatorWithNoRoleSeesThatATaskFailedAndNotWhy(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	alice, op := r.login("alice"), r.login("bob") // bob holds no role: whoever connected the project owns it
	if err := r.hub.SetOperator(ctx, r.operator(), op.ID, true); err != nil {
		t.Fatal(err)
	}
	op, _ = r.store.User(ctx, op.ID)
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.fakes["web"].FailNext("the registry refused the pull")
	if _, err := r.hub.StartSpawn(ctx, alice, r.id("web"), "coder", "fix-bug"); err != nil {
		t.Fatal(err)
	}
	r.hub.WaitForTasks()

	seen, n := r.agentIn(op, r.id("web"), "fix-bug")
	if n != 1 || seen.Task == nil || seen.Task.State != store.TaskFailed {
		t.Fatalf("the operator sees %d %+v", n, seen.Task)
	}
	if seen.Task.Err != "" || seen.Task.Step != "" {
		t.Errorf("an overseer read what the task says: %+v", seen.Task)
	}
	if own, _ := r.agentIn(alice, r.id("web"), "fix-bug"); !strings.Contains(own.Task.Err, "registry refused") {
		t.Errorf("who has a role lost the failure: %+v", own.Task)
	}
}

func TestATaskThatRunsCannotBeDismissedAndAFailureGivesWayToTheNextTask(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder", "reviewer")
	op := r.operator()
	r.hub.Spawn(ctx, op, r.id("web"), "ghost", "again")
	failed, _ := r.agentIn(op, r.id("web"), "again")
	r.fakes["web"].Hold()
	running, err := r.hub.StartSpawn(ctx, op, r.id("web"), "reviewer", "again")
	if err != nil {
		t.Fatal(err)
	}
	a, n := r.agentIn(op, r.id("web"), "again")
	if n != 1 || a.Task.ID != running.ID || a.Template != "reviewer" {
		t.Errorf("the second spawn of a name that failed = %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, op, r.id("web"), running.ID); !errors.Is(err, ErrTaskRunning) {
		t.Errorf("dismissing a task that runs: %v", err)
	}
	if err := r.hub.DismissTask(ctx, op, r.id("web"), failed.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the failure that gave way is still shown: %v", err)
	}
	r.fakes["web"].Release()
	r.hub.WaitForTasks()
}

func TestOneTaskAtATimeOnAnAgentAndItIsPartOfTheAgentForEveryone(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	op := r.operator()
	carol := r.login("carol")
	r.store.SetRole(ctx, r.id("web"), carol.ID, rbac.Admin)
	r.fakes["web"].Hold()

	stop, err := r.hub.StartOperation(ctx, op, VerbStop, r.id("web"), "coder-1")
	if err != nil {
		t.Fatal(err)
	}
	var busy BusyError
	if _, err := r.hub.StartOperation(ctx, carol, VerbRm, r.id("web"), "coder-1"); !errors.As(err, &busy) || busy.Task.ID != stop.ID {
		t.Errorf("a removal while it is being stopped: %v", err)
	}
	// the task is shown next to what the engine says, never instead of it
	a, _ := r.agentIn(carol, r.id("web"), "coder-1")
	if a.State != "running" || a.Task == nil || a.Task.Verb != VerbStop || a.Task.State != store.TaskRunning || a.Task.Actor != rbac.Actor(op.ID) {
		t.Errorf("the agent while it is stopped = %+v %+v", a.Agent, a.Task)
	}
	refs, _ := r.hub.Projects(ctx, carol)
	if len(refs) != 1 || len(refs[0].Tasks) != 1 || refs[0].Tasks[0].ID != stop.ID {
		t.Errorf("the project list does not say what runs: %+v", refs)
	}

	r.fakes["web"].Release()
	r.hub.WaitForTasks()
	a, _ = r.agentIn(carol, r.id("web"), "coder-1")
	if a.State != "exited" || a.Task != nil {
		t.Errorf("the agent once stopped = %+v %+v", a.Agent, a.Task)
	}
	if err := r.hub.Operate(ctx, carol, VerbRm, r.id("web"), "coder-1"); err != nil {
		t.Errorf("a removal once the stop ended: %v", err)
	}
}

func TestTheSpawnsAndRemovalsOfAProjectRunOneAfterTheOtherInOrder(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("old", "operator"))
	op := r.operator()
	r.fakes["web"].Hold()
	first, _ := r.hub.StartSpawn(ctx, op, r.id("web"), "coder", "one")
	removal, _ := r.hub.StartOperation(ctx, op, VerbRm, r.id("web"), "old")
	second, _ := r.hub.StartSpawn(ctx, op, r.id("web"), "coder", "two")
	eventually(t, "the later ones to wait", func() bool {
		a, _ := r.agentIn(op, r.id("web"), "two")
		b, _ := r.agentIn(op, r.id("web"), "old")
		return a.Task.Step == "wait for the task before it" && b.Task.Step == "wait for the task before it"
	})
	r.fakes["web"].Release()
	r.hub.WaitForTasks()
	calls := strings.Join(r.fakes["web"].Calls(), "; ")
	if !strings.HasPrefix(calls, "spawn one user:") || !strings.Contains(calls, "; rm old; spawn two user:") {
		t.Errorf("the engine was asked in order, one at a time: %v", calls)
	}
	for _, id := range []string{first.ID, removal.ID, second.ID} {
		if task, _ := r.hub.TaskOf(ctx, op, r.id("web"), id); task.State != store.TaskDone {
			t.Errorf("%s = %+v", id, task)
		}
	}
}

func TestATaskThatRanWhenTheHubStoppedIsInterruptedAndShownUntilDismissed(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	op := r.operator()
	// what a hub that stopped left behind
	running, _ := r.store.AddTask(ctx, store.Task{Verb: VerbSpawn, Project: r.id("web"), Agent: "half", Template: "coder", Actor: rbac.CLI})
	failed, _ := r.store.AddTask(ctx, store.Task{Verb: VerbSpawn, Project: r.id("web"), Agent: "broken", Template: "coder", Actor: rbac.CLI})
	r.store.EndTask(ctx, failed.ID, store.TaskFailed, "no room", time.Now())

	if err := r.hub.RecoverTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if a, n := r.agentIn(op, r.id("web"), "half"); n != 1 || a.Task.State != store.TaskInterrupted || a.Task.Err == "" || !a.Task.CanDismiss {
		t.Errorf("a spawn the hub stopped during = %d %+v", n, a.Task)
	}
	if a, n := r.agentIn(op, r.id("web"), "broken"); n != 1 || a.Task.State != store.TaskFailed || a.Task.Err != "no room" {
		t.Errorf("a failure nobody dismissed = %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, op, r.id("web"), running.ID); err != nil {
		t.Fatal(err)
	}
	if _, n := r.agentIn(op, r.id("web"), "half"); n != 0 {
		t.Error("a dismissed interrupted spawn is still an agent")
	}
}

func TestTheActivityListsTheTasksOfAProjectOrOfOneAgentNewestFirst(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.link("api", "coder")
	op := r.operator()
	alice := r.login("alice")
	r.hub.Spawn(ctx, op, r.id("web"), "coder", "a")
	r.hub.Spawn(ctx, op, r.id("web"), "coder", "b")
	r.hub.Operate(ctx, op, VerbStop, r.id("web"), "a")
	r.hub.Spawn(ctx, op, r.id("api"), "coder", "c")

	tasks, err := r.hub.Tasks(ctx, op, r.id("web"), "", 10)
	if err != nil || len(tasks) != 3 || tasks[0].Verb != VerbStop || tasks[2].Agent != "a" {
		t.Errorf("the project's activity = %+v %v", tasks, err)
	}
	if tasks, _ := r.hub.Tasks(ctx, op, r.id("web"), "a", 10); len(tasks) != 2 {
		t.Errorf("one agent's = %+v", tasks)
	}
	if _, err := r.hub.Tasks(ctx, alice, r.id("web"), "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("without a role: %v", err)
	}
	if _, err := r.hub.TaskOf(ctx, op, r.id("api"), tasks[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a task read through another project: %v", err)
	}
}
