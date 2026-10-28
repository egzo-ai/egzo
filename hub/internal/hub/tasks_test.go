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
	view, err := r.hub.Project(ctx, user, "local", project)
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
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)
	r.fakes["local"].Hold()

	request, cancel := context.WithCancel(ctx)
	started, err := r.hub.StartSpawn(request, alice, "local", "web", "coder", "fix-bug")
	if err != nil || started.ID == "" || started.State != store.TaskRunning || started.Verb != VerbSpawn {
		t.Fatalf("start: %+v %v", started, err)
	}
	cancel() // the browser, or a proxy in front of the hub, gave up on the request

	// everyone who sees the project sees the agent being made, by whom, and the step it is at
	eventually(t, "the first step", func() bool {
		a, _ := r.agentIn(bob, "web", "fix-bug")
		return a.Task != nil && a.Task.Step == "create network web-fix-bug"
	})
	a, n := r.agentIn(bob, "web", "fix-bug")
	if n != 1 || a.State != "" || a.Template != "coder" || a.Actor != rbac.Actor(alice.ID) || a.Task.ID != started.ID ||
		a.Task.State != store.TaskRunning || a.Task.CanDismiss {
		t.Fatalf("the agent while it is made = %d %+v %+v", n, a, a.Task)
	}

	r.fakes["local"].Release()
	r.hub.WaitForTasks()
	a, n = r.agentIn(alice, "web", "fix-bug")
	if n != 1 || a.State != "running" || a.Actor != rbac.Actor(alice.ID) || a.Task != nil {
		t.Fatalf("the agent once made = %d %+v %+v", n, a, a.Task)
	}
	ended, _ := r.hub.TaskOf(ctx, alice, "local", "web", started.ID)
	if ended.State != store.TaskDone || ended.Ended.IsZero() || ended.Step != "create container web-fix-bug-1" {
		t.Errorf("the task once ended = %+v", ended)
	}
}

func TestASpawnNeedsANameNotTakenARoleAndIsRefusedAtOnce(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("taken", "operator"))
	op := r.operator()
	alice := r.login("alice")
	r.scan()
	if _, err := r.hub.StartSpawn(ctx, op, "local", "web", "coder", ""); !errors.Is(err, ErrNameRequired) {
		t.Errorf("a spawn without a name: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, op, "local", "web", "coder", "taken"); !errors.As(err, new(ExistsError)) {
		t.Errorf("a spawn of a name an agent has: %v", err)
	}
	if _, err := r.hub.StartSpawn(ctx, alice, "local", "web", "coder", "mine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a spawn without a role: %v", err)
	}
	if tasks, _ := r.hub.Tasks(ctx, op, "local", "web", "", 10); len(tasks) != 0 {
		t.Errorf("a refused spawn is a task: %+v", tasks)
	}
}

func TestAFailedSpawnStaysAnAgentWithWhyUntilItIsDismissed(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)

	if err := r.hub.Spawn(ctx, alice, "local", "web", "ghost", "lost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("waiting for a spawn that fails: %v", err)
	}
	// bob sees it, and may not take it away: it is alice's, as the agent would have been
	a, n := r.agentIn(bob, "web", "lost")
	if n != 1 || a.Task == nil || a.Task.State != store.TaskFailed || !strings.Contains(a.Task.Err, "ghost") || a.Task.CanDismiss {
		t.Fatalf("bob sees %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, bob, "local", "web", a.Task.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob dismissing alice's failure: %v", err)
	}
	if a, _ := r.agentIn(alice, "web", "lost"); !a.Task.CanDismiss {
		t.Fatal("alice cannot dismiss her failed spawn")
	}
	if err := r.hub.DismissTask(ctx, alice, "local", "web", a.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, n := r.agentIn(bob, "web", "lost"); n != 0 {
		t.Error("a dismissed failed spawn is still an agent")
	}
	if err := r.hub.DismissTask(ctx, alice, "local", "web", a.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("dismissing it again: %v", err)
	}
	// the activity keeps it
	if tasks, _ := r.hub.Tasks(ctx, bob, "local", "web", "", 10); len(tasks) != 1 || tasks[0].State != store.TaskFailed || !tasks[0].Dismissed {
		t.Errorf("the activity = %+v", tasks)
	}
}

func TestATaskThatRunsCannotBeDismissedAndAFailureGivesWayToTheNextTask(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder", "reviewer"))
	op := r.operator()
	r.scan()
	r.hub.Spawn(ctx, op, "local", "web", "ghost", "again")
	failed, _ := r.agentIn(op, "web", "again")
	r.fakes["local"].Hold()
	running, err := r.hub.StartSpawn(ctx, op, "local", "web", "reviewer", "again")
	if err != nil {
		t.Fatal(err)
	}
	a, n := r.agentIn(op, "web", "again")
	if n != 1 || a.Task.ID != running.ID || a.Template != "reviewer" {
		t.Errorf("the second spawn of a name that failed = %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, op, "local", "web", running.ID); !errors.Is(err, ErrTaskRunning) {
		t.Errorf("dismissing a task that runs: %v", err)
	}
	if err := r.hub.DismissTask(ctx, op, "local", "web", failed.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the failure that gave way is still shown: %v", err)
	}
	r.fakes["local"].Release()
	r.hub.WaitForTasks()
}

func TestOneTaskAtATimeOnAnAgentAndItIsPartOfTheAgentForEveryone(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	op := r.operator()
	carol := r.login("carol")
	r.scan()
	r.store.SetRole(ctx, "local", "web", carol.ID, rbac.Administrator)
	r.fakes["local"].Hold()

	stop, err := r.hub.StartOperation(ctx, op, VerbStop, "local", "web", "coder-1")
	if err != nil {
		t.Fatal(err)
	}
	var busy BusyError
	if _, err := r.hub.StartOperation(ctx, carol, VerbRm, "local", "web", "coder-1"); !errors.As(err, &busy) || busy.Task.ID != stop.ID {
		t.Errorf("a removal while it is being stopped: %v", err)
	}
	// the task is shown next to what the engine says, never instead of it
	a, _ := r.agentIn(carol, "web", "coder-1")
	if a.State != "running" || a.Task == nil || a.Task.Verb != VerbStop || a.Task.State != store.TaskRunning || a.Task.Actor != rbac.Actor(op.ID) {
		t.Errorf("the agent while it is stopped = %+v %+v", a.Agent, a.Task)
	}
	refs, _, _ := r.hub.Projects(ctx, carol)
	if len(refs) != 1 || len(refs[0].Tasks) != 1 || refs[0].Tasks[0].ID != stop.ID {
		t.Errorf("the project list does not say what runs: %+v", refs)
	}

	r.fakes["local"].Release()
	r.hub.WaitForTasks()
	a, _ = r.agentIn(carol, "web", "coder-1")
	if a.State != "exited" || a.Task != nil {
		t.Errorf("the agent once stopped = %+v %+v", a.Agent, a.Task)
	}
	if err := r.hub.Operate(ctx, carol, VerbRm, "local", "web", "coder-1"); err != nil {
		t.Errorf("a removal once the stop ended: %v", err)
	}
}

func TestTheSpawnsAndRemovalsOfAProjectRunOneAfterTheOtherInOrder(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("old", "operator"))
	op := r.operator()
	r.scan()
	r.fakes["local"].Hold()
	first, _ := r.hub.StartSpawn(ctx, op, "local", "web", "coder", "one")
	removal, _ := r.hub.StartOperation(ctx, op, VerbRm, "local", "web", "old")
	second, _ := r.hub.StartSpawn(ctx, op, "local", "web", "coder", "two")
	eventually(t, "the later ones to wait", func() bool {
		a, _ := r.agentIn(op, "web", "two")
		b, _ := r.agentIn(op, "web", "old")
		return a.Task.Step == "wait for the task before it" && b.Task.Step == "wait for the task before it"
	})
	r.fakes["local"].Release()
	r.hub.WaitForTasks()
	calls := strings.Join(r.fakes["local"].Calls(), "; ")
	if !strings.HasPrefix(calls, "spawn web/one user:") || !strings.Contains(calls, "; rm web/old; spawn web/two user:") {
		t.Errorf("the engine was asked in order, one at a time: %v", calls)
	}
	for _, id := range []string{first.ID, removal.ID, second.ID} {
		if task, _ := r.hub.TaskOf(ctx, op, "local", "web", id); task.State != store.TaskDone {
			t.Errorf("%s = %+v", id, task)
		}
	}
}

func TestATaskThatRanWhenTheHubStoppedIsInterruptedAndShownUntilDismissed(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	r.scan()
	// what a hub that stopped left behind
	running, _ := r.store.AddTask(ctx, store.Task{Verb: VerbSpawn, Engine: "local", Project: "web", Agent: "half", Template: "coder", Actor: rbac.Operator})
	failed, _ := r.store.AddTask(ctx, store.Task{Verb: VerbSpawn, Engine: "local", Project: "web", Agent: "broken", Template: "coder", Actor: rbac.Operator})
	r.store.EndTask(ctx, failed.ID, store.TaskFailed, "no room", time.Now())

	if err := r.hub.RecoverTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if a, n := r.agentIn(op, "web", "half"); n != 1 || a.Task.State != store.TaskInterrupted || a.Task.Err == "" || !a.Task.CanDismiss {
		t.Errorf("a spawn the hub stopped during = %d %+v", n, a.Task)
	}
	if a, n := r.agentIn(op, "web", "broken"); n != 1 || a.Task.State != store.TaskFailed || a.Task.Err != "no room" {
		t.Errorf("a failure nobody dismissed = %d %+v", n, a.Task)
	}
	if err := r.hub.DismissTask(ctx, op, "local", "web", running.ID); err != nil {
		t.Fatal(err)
	}
	if _, n := r.agentIn(op, "web", "half"); n != 0 {
		t.Error("a dismissed interrupted spawn is still an agent")
	}
}

func TestTheActivityListsTheTasksOfAProjectOrOfOneAgentNewestFirst(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].AddProject(project("api", "coder"))
	op := r.operator()
	alice := r.login("alice")
	r.scan()
	r.hub.Spawn(ctx, op, "local", "web", "coder", "a")
	r.hub.Spawn(ctx, op, "local", "web", "coder", "b")
	r.hub.Operate(ctx, op, VerbStop, "local", "web", "a")
	r.hub.Spawn(ctx, op, "local", "api", "coder", "c")

	tasks, err := r.hub.Tasks(ctx, op, "local", "web", "", 10)
	if err != nil || len(tasks) != 3 || tasks[0].Verb != VerbStop || tasks[2].Agent != "a" {
		t.Errorf("the project's activity = %+v %v", tasks, err)
	}
	if tasks, _ := r.hub.Tasks(ctx, op, "local", "web", "a", 10); len(tasks) != 2 {
		t.Errorf("one agent's = %+v", tasks)
	}
	if _, err := r.hub.Tasks(ctx, alice, "local", "web", "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("without a role: %v", err)
	}
	if _, err := r.hub.TaskOf(ctx, op, "local", "api", tasks[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a task read through another project: %v", err)
	}
}
