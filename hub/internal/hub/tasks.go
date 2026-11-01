// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	corenames "github.com/egzo-ai/egzo/internal/names"
)

// Every action of the hub on a project (a spawn, a start, a stop, a removal) can take from a second to a minute, longer than a
// browser, or a proxy in front of the hub, waits for an answer. So each is a task: recorded, answered at once, and run in the
// background apart from the request that asked for it. While it runs it is part of its agent, for everyone who sees the project; one
// that failed stays on its agent until somebody dismisses it. Tasks are kept in the database: the project's activity lists them, and
// those that ran when the hub stopped are marked interrupted when it starts again (RecoverTasks), like those whose link dropped while
// they ran: nothing follows them any more. See roadmap/hub-tasks.md.

const (
	// taskDeadline is how long one task may take, waiting for the ones before it included.
	taskDeadline = 10 * time.Minute
	// taskRetention is how long an ended task stays in the project's activity; a failure nobody dismissed stays until it is.
	taskRetention = 30 * 24 * time.Hour
)

// The verbs of a task.
const (
	VerbSpawn = "spawn"
	VerbStart = "start"
	VerbStop  = "stop"
	VerbRm    = "rm"
)

// Task is one action of the hub on a project.
type Task = store.Task

var (
	// ErrNameRequired is returned for a spawn without a name: the hub names the agent before it exists, so that it is one agent from
	// the start.
	ErrNameRequired = errors.New("an agent needs a name")
	// ErrBadName is returned for a spawn of a name an agent cannot have.
	ErrBadName = errors.New("a name is " + corenames.IdentifierRule)
	// ErrTaskRunning is returned for a task that cannot be dismissed because it has not ended.
	ErrTaskRunning = errors.New("the task has not ended")
)

// BusyError is returned for an action on an agent that a task works on already: one at a time.
type BusyError struct{ Task Task }

func (e BusyError) Error() string {
	return fmt.Sprintf("%s: the %s task of %s has not ended", e.Task.Agent, e.Task.Verb, e.Task.Actor)
}

// ExistsError is returned for a spawn of a name an agent of the project has.
type ExistsError struct{ Agent string }

func (e ExistsError) Error() string { return "there is already an agent named " + e.Agent }

// TaskView is a task as a user sees it on its agent.
type TaskView struct {
	Task
	// CanDismiss: it has ended, and the user may operate its agent (rbac.Operate, as for removing it).
	CanDismiss bool
}

type agentKey struct{ project, agent string }

type taskList struct {
	mu   sync.Mutex
	open map[string]*Task         // running, or failed or interrupted and not dismissed; by id
	busy map[agentKey]string      // the running task of each agent
	last map[string]chan struct{} // the newest spawn or removal of each project: the next one waits for it
	done map[string]chan struct{} // by id: closed when it ends
	wg   sync.WaitGroup
}

func (l *taskList) init() {
	if l.open == nil {
		l.open, l.busy, l.last, l.done = map[string]*Task{}, map[agentKey]string{}, map[string]chan struct{}{}, map[string]chan struct{}{}
	}
}

// RecoverTasks marks what ran when the hub stopped as interrupted, and shows on their agents the failures nobody dismissed. Every project
// is offline until its link dials again. Call it before links are taken.
func (h *Hub) RecoverTasks(ctx context.Context) error {
	if err := h.store.InterruptTasks(ctx, time.Now()); err != nil {
		return err
	}
	if err := h.store.AllOffline(ctx, time.Now()); err != nil {
		return err
	}
	open, err := h.store.OpenTasks(ctx)
	if err != nil {
		return err
	}
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	h.tasks.init()
	for _, t := range open {
		h.tasks.open[t.ID] = &t
	}
	return nil
}

// StartSpawn checks that the user may spawn from the project and starts a task that makes the agent, which belongs to the user. It
// returns before the agent exists; the task goes on if the request that asked for it goes away.
func (h *Hub) StartSpawn(ctx context.Context, user store.User, project, template, name string) (Task, error) {
	if name == "" {
		return Task{}, ErrNameRequired
	}
	if !corenames.Instance(name) {
		return Task{}, ErrBadName
	}
	if _, err := h.access(ctx, user, project); err != nil {
		return Task{}, err
	}
	if c := h.connected(project); c == nil || !c.served.Load() {
		return Task{}, h.offline(ctx, project)
	}
	if p, ok := h.knownProject(project); ok {
		for _, a := range p.Agents {
			if a.Name == name {
				return Task{}, ExistsError{Agent: name}
			}
		}
	}
	if t, ok := h.busyTask(project, name); ok {
		return Task{}, BusyError{Task: t}
	}
	if _, err := h.authorize(ctx, user, project, name, rbac.Spawn, "", "agent.spawn"); err != nil {
		return Task{}, err
	}
	t := Task{Verb: VerbSpawn, Project: project, Agent: name, Template: template, Actor: rbac.Actor(user.ID)}
	// one at a time per project, in order: the engine's project lock would make them wait for each other anyway, and not in order
	return h.startTask(ctx, t, true, func(ctx context.Context, e links.Engine, progress func(string)) error {
		_, err := e.Spawn(ctx, template, name, t.Actor, progress)
		return err
	})
}

// StartOperation checks that the user may start, stop or remove the agent, and starts a task that does it.
func (h *Hub) StartOperation(ctx context.Context, user store.User, verb, project, agent string) (Task, error) {
	if verb != VerbStart && verb != VerbStop && verb != VerbRm {
		return Task{}, fmt.Errorf("unknown operation %q", verb)
	}
	if _, err := h.access(ctx, user, project); err != nil {
		return Task{}, err
	}
	if t, ok := h.busyTask(project, agent); ok {
		return Task{}, BusyError{Task: t}
	}
	creator, err := h.creatorOf(ctx, project, agent)
	if err != nil {
		return Task{}, err
	}
	if _, err := h.authorize(ctx, user, project, agent, rbac.Operate, creator, "agent."+verb); err != nil {
		return Task{}, err
	}
	t := Task{Verb: verb, Project: project, Agent: agent, Actor: rbac.Actor(user.ID)}
	// a removal takes the project's lock, as a spawn does
	return h.startTask(ctx, t, verb == VerbRm, func(ctx context.Context, e links.Engine, progress func(string)) error {
		switch verb {
		case VerbStart:
			return e.Start(ctx, agent)
		case VerbStop:
			return e.Stop(ctx, agent)
		}
		return e.Remove(ctx, agent, progress)
	})
}

type taskFunc func(ctx context.Context, e links.Engine, progress func(string)) error

// startTask records the task and runs it. A queued one waits for the queued task of the project before it.
func (h *Hub) startTask(ctx context.Context, t Task, queued bool, do taskFunc) (Task, error) {
	key, project := agentKey{t.Project, t.Agent}, t.Project
	h.tasks.mu.Lock()
	h.tasks.init()
	if id, ok := h.tasks.busy[key]; ok { // asked for twice at once
		busy := *h.tasks.open[id]
		h.tasks.mu.Unlock()
		return Task{}, BusyError{Task: busy}
	}
	t, err := h.store.AddTask(ctx, t)
	if err != nil {
		h.tasks.mu.Unlock()
		return Task{}, err
	}
	// a failure shown on the agent gives way to what is done on it now; the activity keeps it
	for id, old := range h.tasks.open {
		if (agentKey{old.Project, old.Agent}) == key && old.Over() {
			delete(h.tasks.open, id)
			if err := h.store.DismissTask(ctx, id); err != nil {
				h.log.Warn("dismiss a task", "task", id, "err", err)
			}
		}
	}
	task := new(Task) // the goroutine's, under the lock; t is what the caller gets
	*task = t
	done := make(chan struct{})
	h.tasks.open[t.ID], h.tasks.busy[key], h.tasks.done[t.ID] = task, t.ID, done
	var before chan struct{}
	if queued {
		before = h.tasks.last[project]
		h.tasks.last[project] = done
	}
	h.tasks.mu.Unlock()

	h.tasks.wg.Add(1)
	go func() {
		defer h.tasks.wg.Done()
		defer close(done)
		run, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskDeadline)
		defer cancel()
		progress := func(step string) { h.setStep(task, step) }
		if before != nil {
			progress("wait for the task before it")
			select {
			case <-before:
			case <-run.Done():
			}
		}
		err := run.Err()
		if err == nil {
			err = h.call(run, t.Project, func(ctx context.Context, e links.Engine) error { return do(ctx, e, progress) })
		}
		h.endTask(task, done, err)
	}()
	return t, nil
}

func (h *Hub) setStep(task *Task, step string) {
	h.tasks.mu.Lock()
	task.Step = step
	id := task.ID
	h.tasks.mu.Unlock()
	if err := h.store.SetTaskStep(context.Background(), id, step); err != nil {
		h.log.Warn("record a task's step", "task", id, "err", err)
	}
}

func (h *Hub) endTask(task *Task, done chan struct{}, err error) {
	h.tasks.mu.Lock()
	task.State, task.Ended = store.TaskDone, time.Now()
	switch {
	case errors.As(err, new(OfflineError)):
		task.State, task.Err = store.TaskInterrupted, "the link dropped while it ran: look at the agent to see where it got"
	case err != nil:
		task.State, task.Err = store.TaskFailed, err.Error()
	}
	t := *task
	delete(h.tasks.busy, agentKey{t.Project, t.Agent})
	delete(h.tasks.done, t.ID)
	if t.State == store.TaskDone {
		delete(h.tasks.open, t.ID)
	}
	if h.tasks.last[t.Project] == done {
		delete(h.tasks.last, t.Project)
	}
	h.tasks.mu.Unlock()
	if err != nil {
		h.log.Warn("task failed", "verb", t.Verb, "project", t.Project, "agent", t.Agent, "actor", t.Actor, "err", err)
	}
	if err := h.store.EndTask(context.Background(), t.ID, t.State, t.Err, t.Ended); err != nil {
		h.log.Warn("record the end of a task", "task", t.ID, "err", err)
	}
}

// Spawn spawns an agent and waits until it exists or the task has failed: StartSpawn, for a caller that has the time.
func (h *Hub) Spawn(ctx context.Context, user store.User, project, template, name string) error {
	t, err := h.StartSpawn(ctx, user, project, template, name)
	if err != nil {
		return err
	}
	return h.waitTask(ctx, t.ID)
}

// Operate starts, stops or removes an agent and waits until it is done: StartOperation, for a caller that has the time.
func (h *Hub) Operate(ctx context.Context, user store.User, verb, project, agent string) error {
	t, err := h.StartOperation(ctx, user, verb, project, agent)
	if err != nil {
		return err
	}
	return h.waitTask(ctx, t.ID)
}

func (h *Hub) waitTask(ctx context.Context, id string) error {
	h.tasks.mu.Lock()
	done := h.tasks.done[id]
	h.tasks.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t, err := h.store.TaskByID(ctx, id)
	if err != nil {
		return err
	}
	if t.State != store.TaskDone {
		return taskError(t.Err)
	}
	return nil
}

// WaitForTasks waits until every task that was started has ended.
func (h *Hub) WaitForTasks() { h.tasks.wg.Wait() }

type taskError string

func (e taskError) Error() string { return string(e) }

// busyTask is the task that runs on an agent.
func (h *Hub) busyTask(project, agent string) (Task, bool) {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	if id, ok := h.tasks.busy[agentKey{project, agent}]; ok {
		return *h.tasks.open[id], true
	}
	return Task{}, false
}

// openTasks are the tasks shown on the agents of a project, oldest first.
func (h *Hub) openTasks(project string) []Task {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	var list []Task
	for _, t := range h.tasks.open {
		if t.Project == project {
			list = append(list, *t)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Started.Equal(list[j].Started) {
			return list[i].Started.Before(list[j].Started)
		}
		return list[i].ID < list[j].ID
	})
	return list
}

// runningTasks are the tasks that run on the agents of a project, oldest first.
func (h *Hub) runningTasks(project string) []Task {
	var list []Task
	for _, t := range h.openTasks(project) {
		if !t.Over() {
			list = append(list, t)
		}
	}
	return list
}

// withTasks puts on each agent what the hub does on it, or the failure nobody dismissed; the latest wins. An agent the project does
// not have (yet, or any more) is shown while a task runs on it, and a spawn that failed is shown until it is dismissed: a spawn is one
// agent from when it was asked for, and a removal does not leave a gap before it ends.
func (h *Hub) withTasks(user store.User, role rbac.Role, project string, agents []AgentView) []AgentView {
	index := map[string]int{}
	for i, a := range agents {
		index[a.Name] = i
	}
	for _, t := range h.openTasks(project) {
		i, ok := index[t.Agent]
		if !ok {
			if t.Over() && t.Verb != VerbSpawn {
				continue // it failed on an agent that is gone since: the activity has it
			}
			agents = append(agents, AgentView{Agent: links.Agent{Name: t.Agent, Template: t.Template, Actor: t.Actor},
				CanOperate: rbac.Can(user.Subject(), role, rbac.Operate, t.Actor)})
			i = len(agents) - 1
			index[t.Agent] = i
		}
		creator := agents[i].Actor
		if !rbac.Can(user.Subject(), role, rbac.Inspect, "") {
			t.Step, t.Err = "", "" // an operator with no role sees that it runs or failed: what it says is the tasks', which are for a role
		}
		agents[i].Task = &TaskView{Task: t, CanDismiss: t.Over() && rbac.Can(user.Subject(), role, rbac.Operate, creator)}
	}
	return agents
}

// DismissTask takes an ended task off its agent, for a failure somebody read; it stays in the project's activity. Who may is who may
// operate the agent. A task that runs cannot be: it ends on its own.
func (h *Hub) DismissTask(ctx context.Context, user store.User, project, id string) error {
	if _, err := h.access(ctx, user, project); err != nil {
		return err
	}
	h.tasks.mu.Lock()
	h.tasks.init()
	open, ok := h.tasks.open[id]
	var t Task
	if ok {
		t = *open
	}
	h.tasks.mu.Unlock()
	if !ok || t.Project != project {
		return ErrNotFound
	}
	if !t.Over() {
		return ErrTaskRunning
	}
	creator := t.Actor
	if p, ok := h.knownProject(project); ok {
		for _, a := range p.Agents {
			if a.Name == t.Agent {
				creator = a.Actor
			}
		}
	}
	if _, err := h.authorize(ctx, user, project, t.Agent, rbac.Operate, creator, "task.dismiss"); err != nil {
		return err
	}
	if err := h.store.DismissTask(ctx, id); err != nil && !errors.Is(err, store.ErrNoTask) {
		return err
	}
	h.tasks.mu.Lock()
	delete(h.tasks.open, id)
	h.tasks.mu.Unlock()
	return nil
}

// forgetTasks drops what is shown of a project that is gone. A task that still runs ends on its own.
func (h *Hub) forgetTasks(project string) {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	for id, t := range h.tasks.open {
		if t.Project == project && t.Over() {
			delete(h.tasks.open, id)
		}
	}
}

// Tasks lists the latest tasks of a project, or of one of its agents, newest first: the project's activity.
func (h *Hub) Tasks(ctx context.Context, user store.User, project, agent string, limit int) ([]Task, error) {
	if _, err := h.inspect(ctx, user, project); err != nil {
		return nil, err
	}
	return h.store.TasksOf(ctx, project, agent, limit)
}

// TaskOf reads one task of a project.
func (h *Hub) TaskOf(ctx context.Context, user store.User, project, id string) (Task, error) {
	if _, err := h.inspect(ctx, user, project); err != nil {
		return Task{}, err
	}
	t, err := h.store.TaskByID(ctx, id)
	if errors.Is(err, store.ErrNoTask) || err == nil && t.Project != project {
		return Task{}, ErrNotFound
	}
	return t, err
}
