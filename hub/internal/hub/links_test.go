// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// waiting is a project that dialled the hub and was told to wait for a person.
func (r *rig) waiting(name string) (*fake.Engine, store.Registration) {
	r.t.Helper()
	f := fake.New(name, "laptop", "coder")
	r.connect(f)
	reg, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil {
		r.t.Fatal(err)
	}
	return f, reg
}

func last(f *fake.Engine) links.Verdict {
	verdicts := f.Verdicts()
	return verdicts[len(verdicts)-1]
}

func TestALinkWithANewKeyWaitsForAPersonAndSaysWhere(t *testing.T) {
	r := newRig(t)
	f, reg := r.waiting("web")
	if reg.State != hublink.StatePending || reg.Host != "laptop" || len(reg.Templates) != 1 || len(reg.Code) != 9 {
		t.Fatalf("registration = %+v", reg)
	}
	verdict := last(f)
	if verdict.State != hublink.StatePending || verdict.Code != reg.Code || verdict.Address != "https://hub.example.org/link/"+reg.Code {
		t.Errorf("the link was told %+v", verdict)
	}
	// nobody sees it, nothing is asked of it, and nothing it pushes is kept
	op := r.operator()
	if refs, _ := r.hub.Projects(ctx, op); len(refs) != 0 {
		t.Errorf("a pending project is listed: %s", names(refs))
	}
	if _, err := r.hub.Project(ctx, op, reg.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pending project's page: %v", err)
	}
	f.PushProject(links.ProjectUpdate{Project: links.Project{Name: "web", Up: true}})
	if _, known := r.hub.knownProject(reg.ID); known {
		t.Error("what a pending link pushed was kept")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("a pending project was asked %v", f.Calls())
	}
}

func TestOnlyAnOperatorApprovesAndWhoeverDoesOwnsIt(t *testing.T) {
	r := newRig(t)
	f, reg := r.waiting("web")
	alice, op := r.login("alice"), r.operator()
	if _, err := r.hub.Pending(ctx, alice, reg.Code); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user reading a pending project: %v", err)
	}
	if _, err := r.hub.Approve(ctx, alice, reg.Code); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user approving: %v", err)
	}
	if err := r.hub.Refuse(ctx, alice, reg.Code, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user refusing: %v", err)
	}
	if got := last(f); got.State != hublink.StatePending {
		t.Fatalf("a refused attempt told the link %+v", got)
	}
	shown, err := r.hub.Pending(ctx, op, strings.ToLower(reg.Code)) // typed by hand
	if err != nil || shown.ID != reg.ID {
		t.Fatalf("the page of the code = %+v, %v", shown, err)
	}
	approved, err := r.hub.Approve(ctx, op, reg.Code)
	if err != nil || approved.State != hublink.StateApproved || approved.Owner != op.ID {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	eventually(t, "the link to hear it", f.Served)
	if verdict := last(f); verdict.State != hublink.StateApproved || verdict.Project != reg.ID || verdict.Owner != "admin" {
		t.Errorf("the link was told %+v", verdict)
	}
	if role := r.role(op, reg.ID); role != rbac.Owner {
		t.Errorf("who approved holds %q", role)
	}
	if refs, _ := r.hub.Projects(ctx, op); len(refs) != 1 || refs[0].ID != reg.ID || refs[0].Host != "laptop" {
		t.Errorf("projects = %+v", refs)
	}
	// a code is good once
	if _, err := r.hub.Approve(ctx, op, reg.Code); !errors.Is(err, ErrNotFound) {
		t.Errorf("a code used twice: %v", err)
	}
	entries, _ := r.store.AuditOf(ctx, reg.ID, 5)
	if len(entries) != 1 || entries[0].Action != "project.approve" || !entries[0].Allowed {
		t.Errorf("audit = %+v", entries)
	}
}

func TestTheOwnerStaysOwnerWhenTheyStopBeingAnOperator(t *testing.T) {
	r := newRig(t)
	f, reg := r.waiting("web")
	op, alice := r.operator(), r.login("alice")
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	if _, err := r.hub.Approve(ctx, alice, reg.Code); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the link to hear it", f.Served)
	if err := r.hub.SetOperator(ctx, op, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	view, err := r.hub.Project(ctx, alice, reg.ID)
	if err != nil || view.Role != rbac.Owner || view.Operator {
		t.Errorf("the owner's view = %+v, %v", view, err)
	}
}

func TestARefusedProjectIsToldAndStaysConnectedToSaySo(t *testing.T) {
	r := newRig(t)
	f, reg := r.waiting("web")
	op := r.operator()
	if err := r.hub.Refuse(ctx, op, reg.Code, "not one of ours"); err != nil {
		t.Fatal(err)
	}
	if verdict := last(f); verdict.State != hublink.StateRefused || verdict.Message != "not one of ours" {
		t.Errorf("the link was told %+v", verdict)
	}
	select {
	case <-f.Closed():
		t.Error("the connection was closed: the link would call the hub unreachable")
	default:
	}
	// it comes back with the same key: still refused
	f.Drop()
	eventually(t, "the connection to end", func() bool { return r.hub.connected(reg.ID) == nil })
	r.connect(f)
	if verdict := last(f); verdict.State != hublink.StateRefused {
		t.Errorf("a refused key that came back was told %+v", verdict)
	}
	// an operator can take it back: the same link is then a new project
	if err := r.hub.Forget(ctx, op, reg.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the connection to end", func() bool { return r.hub.connected(reg.ID) == nil })
	r.connect(f)
	again, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil || again.ID == reg.ID || again.State != hublink.StatePending {
		t.Errorf("after forgetting: %+v, %v", again, err)
	}
}

func TestAProjectThatIsLinkedCannotBeForgottenOutright(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	if err := r.hub.Forget(ctx, r.operator(), r.id("web")); !errors.Is(err, ErrNotTurnedAway) {
		t.Errorf("forget a linked project: %v", err)
	}
}

func TestRevokingTakesAProjectOffTheHubAndEndsWhatRunsOverItsLink(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(agent("coder-1", "operator"))
	op, id := r.operator(), r.id("web")
	att, err := r.hub.Attach(ctx, op, id, "coder-1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer att.Close()
	input, keep := io.Pipe()
	defer keep.Close()
	ended := make(chan error, 1)
	go func() {
		ended <- att.Run(ctx, links.Terminal{In: input, Out: io.Discard, Size: links.Size{Cols: 80, Rows: 24}})
	}()
	eventually(t, "the terminal", func() bool { return strings.Join(f.Calls(), " ") == "attach coder-1" })

	alice := r.login("alice")
	if err := r.hub.Revoke(ctx, alice, id, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user revoking: %v", err)
	}
	if err := r.hub.Revoke(ctx, op, id, "the laptop was stolen"); err != nil {
		t.Fatal(err)
	}
	if verdict := last(f); verdict.State != hublink.StateRevoked || verdict.Message != "the laptop was stolen" {
		t.Errorf("the link was told %+v", verdict)
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the terminal outlived the project's approval")
	}
	if refs, _ := r.hub.Projects(ctx, op); len(refs) != 0 {
		t.Errorf("a revoked project is listed: %s", names(refs))
	}
	if _, err := r.hub.Project(ctx, op, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("a revoked project's page: %v", err)
	}
	f.PushProject(links.ProjectUpdate{Project: links.Project{Name: "web", Up: true}})
	if _, known := r.hub.knownProject(id); known {
		t.Error("what a revoked link pushed was kept")
	}
	before := len(f.Calls())
	f.Drop()
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	r.connect(f)
	if verdict := last(f); verdict.State != hublink.StateRevoked {
		t.Errorf("a revoked key that came back was told %+v", verdict)
	}
	if len(f.Calls()) != before {
		t.Errorf("a revoked project was asked %v", f.Calls()[before:])
	}
}

func TestAProjectOfflineIsShownOfflineSinceWithWhatWasLastKnownAndAsksNothing(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(agent("coder-1", "operator"))
	op, id := r.operator(), r.id("web")
	eventually(t, "the feed", func() bool { _, feed := r.agentOf(op, id, "coder-1"); return feed.State == FeedLive })

	before := time.Now().Add(-time.Second)
	f.Drop()
	eventually(t, "the hub to notice", func() bool { return r.hub.connected(id) == nil })
	eventually(t, "the time to be recorded", func() bool {
		reg, _ := r.store.Registration(ctx, id)
		return !reg.Online()
	})
	view, err := r.hub.Project(ctx, op, id)
	if err != nil || view.Offline.Before(before) || view.Feed.State != FeedOffline || view.CanSpawn {
		t.Fatalf("offline project = %+v, %v", view, err)
	}
	if len(view.Agents) != 1 || view.Agents[0].Name != "coder-1" || view.Agents[0].Activity != ActivityUnknown {
		t.Errorf("what was last known = %+v", view.Agents)
	}
	var offline OfflineError
	if err := r.hub.Spawn(ctx, op, id, "coder", "more"); !errors.As(err, &offline) || !offline.Since.Equal(view.Offline) {
		t.Errorf("spawn offline: %v", err)
	}
	if _, err := r.hub.StartOperation(ctx, op, VerbStop, id, "coder-1"); !errors.As(err, &offline) {
		t.Errorf("stop offline: %v", err)
	}
	if _, err := r.hub.Attach(ctx, op, id, "coder-1", false); !errors.As(err, &offline) {
		t.Errorf("attach offline: %v", err)
	}
	if _, err := r.hub.Events(ctx, op, id, "", 5); !errors.As(err, &offline) {
		t.Errorf("events offline: %v", err)
	}
	if _, err := r.hub.Spec(ctx, op, id); !errors.As(err, &offline) {
		t.Errorf("spec offline: %v", err)
	}

	r.connect(f)
	eventually(t, "the project to be back", func() bool {
		view, _ := r.hub.Project(ctx, op, id)
		return view.Offline.IsZero() && view.Feed.State == FeedLive && view.CanSpawn
	})
	if verdict := last(f); verdict.State != hublink.StateApproved {
		t.Errorf("a project that came back was told %+v", verdict)
	}
}

func TestATaskThatRanWhenTheLinkDroppedIsInterruptedAndTheProjectGoesOn(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	f.Hold()
	task, err := r.hub.StartSpawn(ctx, op, id, "coder", "fix-bug")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := r.hub.StartSpawn(ctx, op, id, "coder", "after")
	if err != nil {
		t.Fatal(err)
	}
	f.Drop()
	r.hub.WaitForTasks()
	for _, one := range []Task{task, queued} {
		ended, _ := r.hub.TaskOf(ctx, op, id, one.ID)
		if ended.State != store.TaskInterrupted || !strings.Contains(ended.Err, "link dropped") || ended.Ended.IsZero() {
			t.Errorf("a task over a dropped link = %+v", ended)
		}
	}
	// it stays on its agent, for someone to look at, until it is dismissed
	a, _ := r.agentIn(op, id, "fix-bug")
	if a.Task == nil || a.Task.State != store.TaskInterrupted || !a.Task.CanDismiss {
		t.Errorf("the agent = %+v %+v", a, a.Task)
	}
	f.Release()
	r.connect(f)
	eventually(t, "the project to be back", func() bool { view, _ := r.hub.Project(ctx, op, id); return view.CanSpawn })
	if err := r.hub.Spawn(ctx, op, id, "coder", "fix-bug"); err != nil {
		t.Errorf("a task after the link came back: %v", err)
	}
}

func TestATerminalOverALinkThatDropsSaysSo(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(agent("coder-1", "operator"))
	op, id := r.operator(), r.id("web")
	att, err := r.hub.Attach(ctx, op, id, "coder-1", false)
	if err != nil {
		t.Fatal(err)
	}
	input, keep := io.Pipe()
	defer keep.Close()
	ended := make(chan error, 1)
	go func() {
		ended <- att.Run(ctx, links.Terminal{In: input, Out: io.Discard, Size: links.Size{Cols: 80, Rows: 24}})
	}()
	eventually(t, "the terminal", func() bool { return len(f.Calls()) > 0 })
	f.Drop()
	select {
	case err := <-ended:
		if !errors.As(err, new(OfflineError)) {
			t.Errorf("the terminal ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the terminal outlived its link")
	}
	att.Close()
	if holder := r.hub.holder(id, "coder-1"); holder != "" {
		t.Errorf("the seat is still held by %q", holder)
	}
}

func TestTwoConnectionsOfOneKeyAreOneLinkAndTheNewestWins(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	first := r.hub.connected(id)
	again := f.Again() // the link came back before its first connection was noticed gone
	r.connect(again)
	eventually(t, "the new connection", func() bool { c := r.hub.connected(id); return c != first && c.served.Load() })
	eventually(t, "the feeds", again.Served)
	select {
	case <-first.link.Closed():
	case <-time.After(3 * time.Second):
		t.Fatal("the old connection was left open")
	}
	time.Sleep(20 * time.Millisecond) // what ends the old connection must not mark the project offline
	if reg, _ := r.store.Registration(ctx, id); !reg.Online() {
		t.Errorf("the project is offline since %v with a connection up", reg.Offline)
	}
	if view, err := r.hub.Project(ctx, op, id); err != nil || !view.CanSpawn {
		t.Errorf("view = %+v, %v", view, err)
	}
}

func TestALinkThatUnlinksIsForgottenWithItsRolesAndComesBackNew(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id, alice := r.operator(), r.id("web"), r.login("alice")
	if err := r.hub.SetRole(ctx, op, id, alice.ID, rbac.User); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.Spawn(ctx, op, id, "coder", "coder-1"); err != nil {
		t.Fatal(err)
	}
	if verdict := f.Unlink(); verdict.State != hublink.StateUnlinked {
		t.Fatalf("the link was told %+v", verdict)
	}
	if refs, _ := r.hub.Projects(ctx, op); len(refs) != 0 {
		t.Errorf("an unlinked project is listed: %s", names(refs))
	}
	if _, err := r.store.Registration(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("registration: %v", err)
	}
	if grants, _ := r.store.GrantsOf(ctx, alice.ID); len(grants) != 0 {
		t.Errorf("a role outlived its project: %+v", grants)
	}
	if tasks, _ := r.store.TasksOf(ctx, id, "", 10); len(tasks) != 0 {
		t.Errorf("tasks outlived their project: %+v", tasks)
	}
	if entries, _ := r.store.AuditOf(ctx, id, 10); len(entries) == 0 || entries[0].Action != "project.unlink" {
		t.Errorf("the audit log = %+v", entries)
	}
	f.Drop()
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	r.connect(f)
	again, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil || again.ID == id || again.State != hublink.StatePending {
		t.Errorf("registered again: %+v, %v", again, err)
	}
}

func TestTwoProjectsOfOneNameAreTwoProjects(t *testing.T) {
	r := newRig(t)
	one := r.adopt(fake.New("web", "laptop", "coder"))
	idOne := r.id("web")
	two := r.adopt(fake.New("web", "server", "coder"))
	idTwo := r.id("web")
	one.SetAgent(agent("only-here", "operator"))
	op := r.operator()
	if view, err := r.hub.Project(ctx, op, idTwo); err != nil || len(view.Agents) != 0 || view.Host != "server" {
		t.Errorf("the other web = %+v, %v", view, err)
	}
	if err := r.hub.Spawn(ctx, op, idTwo, "coder", "there"); err != nil {
		t.Fatal(err)
	}
	if len(one.Calls()) != 0 || len(two.Calls()) != 1 {
		t.Errorf("calls: %v and %v", one.Calls(), two.Calls())
	}
	if view, _ := r.hub.Project(ctx, op, idOne); len(view.Agents) != 1 || view.Agents[0].Name != "only-here" {
		t.Errorf("the first web = %+v", view.Agents)
	}
}

func TestWhatALinkSaysOfItselfIsBounded(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	huge := strings.Repeat("x", 64<<10)
	update := links.ProjectUpdate{Project: links.Project{Name: "somebody-elses-project", Up: true,
		Services: []links.Service{{Name: "control", State: "running"}, {Name: "bitcoin", State: "running"}},
		Agents:   []links.Agent{{Name: "../../etc", State: "running"}, {Name: "fine", State: "running\x1b[31m", Status: huge}},
		Ignored:  []string{huge}}}
	f.PushProject(update)
	view, err := r.hub.Project(ctx, op, id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Name != "web" {
		t.Errorf("a link renamed its project to %q", view.Project.Name)
	}
	if len(view.Agents) != 1 || view.Agents[0].Name != "fine" || strings.Contains(view.Agents[0].State, "\x1b") {
		t.Errorf("agents = %+v", view.Agents)
	}
	if len(view.Project.Services) != 1 || len(view.Project.Ignored[0]) > maxText {
		t.Errorf("project = %+v", view.Project)
	}
	line := links.StatusLine{Time: time.Now(), Agents: []links.AgentStatus{{Agent: "fine", Status: huge, Activity: "working"}, {Agent: "no/such name"}}}
	f.PushStatus(line)
	if view, _ = r.hub.Project(ctx, op, id); len(view.Agents[0].Status) > maxText {
		t.Errorf("a status of %d bytes was kept", len(view.Agents[0].Status))
	}
	many := links.Project{}
	for i := 0; i < 3*maxAgents; i++ {
		many.Agents = append(many.Agents, links.Agent{Name: "a", State: "running"})
	}
	if got := cleanProject(many); len(got.Agents) != maxAgents {
		t.Errorf("%d agents were kept", len(got.Agents))
	}
}

func TestALinkThatNamesNoProjectEgzoCouldMakeIsTurnedAwayAndWhatItSaysIsClipped(t *testing.T) {
	r := newRig(t)
	bad := fake.New("not a name", "laptop")
	if err := r.hub.Link(ctx, bad.Hello(), bad.Connect(), "https://hub"); !errors.Is(err, ErrBadHello) {
		t.Errorf("a bad name: %v", err)
	}
	if regs, _ := r.store.Registrations(ctx); len(regs) != 0 {
		t.Errorf("it was kept: %+v", regs)
	}
	hello := cleanHello(links.Hello{Host: strings.Repeat("h", 1000) + "\x1b[2J", Release: strings.Repeat("r", 1000), Templates: []string{"coder", "no/such", strings.Repeat("t", 1000)}})
	if len(hello.Host) > maxHost || len(hello.Release) > maxWord || len(hello.Templates) != 1 {
		t.Errorf("hello = %+v", hello)
	}
}

func TestTheHubRestartingLeavesEveryProjectOfflineUntilItsLinkDials(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	id := r.id("web")
	f.Drop()
	eventually(t, "the hub to notice", func() bool { return r.hub.connected(id) == nil })
	r.store.Connected(ctx, id, "web", "laptop", "dev") // as the hub that stopped left it
	if err := r.hub.RecoverTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if reg, _ := r.store.Registration(ctx, id); reg.Online() {
		t.Error("a project with no connection is online")
	}
}
