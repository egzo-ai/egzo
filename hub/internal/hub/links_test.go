// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

func last(f *fake.Engine) links.Verdict {
	verdicts := f.Verdicts()
	return verdicts[len(verdicts)-1]
}

// nothingStored says a link left no trace: no project, no audit entry, no connection, no code spent.
func (r *rig) nothingStored(codes int) {
	r.t.Helper()
	if regs, _ := r.store.Registrations(ctx); len(regs) != 0 {
		r.t.Errorf("a project was made: %+v", regs)
	}
	if entries, _ := r.store.AuditOf(ctx, "", 50); len(entries) != 0 {
		r.t.Errorf("the audit log grew: %+v", entries)
	}
	r.hub.mu.RLock()
	defer r.hub.mu.RUnlock()
	if len(r.hub.conns) != 0 {
		r.t.Errorf("%d connections are held", len(r.hub.conns))
	}
	if got, _ := r.store.LinkCodes(ctx, "", time.Now()); len(got) != codes {
		r.t.Errorf("%d codes are left, want %d", len(got), codes)
	}
}

func TestALinkWithoutACodeIsToldItIsUnregisteredAndLeavesNothing(t *testing.T) {
	r := newRig(t)
	f := fake.New("web", "laptop", "coder")
	if err := r.turnedAway(f, ""); !errors.Is(err, ErrUnregistered) {
		t.Errorf("Link ended with %v", err)
	}
	if verdict := last(f); verdict.State != hublink.StateUnregistered || verdict.Message != "" || verdict.Project != "" {
		t.Errorf("the link was told %+v", verdict)
	}
	r.nothingStored(0)
	if f.Running() || len(f.Calls()) != 0 {
		t.Errorf("a stranger was taken: running %v, calls %v", f.Running(), f.Calls())
	}
}

func TestEveryCodeThatFailsFailsTheSameAndLeavesNothing(t *testing.T) {
	r := newRig(t)
	op, alice := r.operator(), r.login("alice")
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	spent := r.code()
	r.dial(fake.New("first", "laptop"), spent)
	eventually(t, "the code to be spent", func() bool { regs, _ := r.store.Registrations(ctx); return len(regs) == 1 })
	cancelled, cancelledCode, _ := r.hub.CreateLinkCode(ctx, op)
	if err := r.hub.CancelLinkCode(ctx, op, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	_, expired, _ := r.store.CreateLinkCode(ctx, op.ID, time.Now().Add(-store.LinkCodeLife-time.Second))
	_, former, _ := r.hub.CreateLinkCode(ctx, alice)
	if err := r.hub.SetOperator(ctx, op, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	var told []links.Verdict
	for name, code := range map[string]string{"wrong": store.LinkCodePrefix + "nonsense", "used": spent, "cancelled": cancelledCode, "expired": expired, "of a former operator": former} {
		f := fake.New("web", "laptop", "coder")
		if err := r.turnedAway(f, code); !errors.Is(err, ErrBadCode) {
			t.Errorf("a code that is %s: %v", name, err)
		}
		told = append(told, last(f))
	}
	for _, verdict := range told {
		if verdict != (links.Verdict{State: hublink.StateUnregistered}) {
			t.Errorf("the link was told %+v: there is nothing to tell one failure from another", verdict)
		}
	}
	if regs, _ := r.store.Registrations(ctx); len(regs) != 1 {
		t.Errorf("registrations = %+v", regs)
	}
}

func TestAGoodCodeMakesAnApprovedProjectOwnedByWhoeverMadeIt(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	code := r.code()
	f := fake.New("web", "laptop", "coder")
	r.dial(f, code)
	r.held(f)
	eventually(t, "the link to hear it", f.Served)
	reg, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil || reg.State != hublink.StateApproved || reg.Owner != op.ID || reg.Host != "laptop" || len(reg.Templates) != 1 {
		t.Fatalf("registration = %+v, %v", reg, err)
	}
	if verdict := last(f); verdict.State != hublink.StateApproved || verdict.Project != reg.ID || verdict.Owner != "admin" {
		t.Errorf("the link was told %+v", verdict)
	}
	if role := r.role(op, reg.ID); role != rbac.Owner {
		t.Errorf("who made the code holds %q", role)
	}
	if refs, _ := r.hub.Projects(ctx, op); len(refs) != 1 || refs[0].ID != reg.ID {
		t.Errorf("projects = %+v", refs)
	}
	entries, _ := r.store.AuditOf(ctx, reg.ID, 5)
	if len(entries) != 1 || entries[0].Action != "project.register" || !entries[0].Allowed || entries[0].Actor != rbac.Actor(op.ID) {
		t.Errorf("audit = %+v", entries)
	}
	if got, _ := r.store.LinkCodes(ctx, "", time.Now()); len(got) != 0 {
		t.Errorf("the code is still good: %+v", got)
	}
}

func TestCodesAreOfWhoMayConnectAProjectAndEachLeavesATrace(t *testing.T) {
	r := newRig(t)
	alice, op := r.login("alice"), r.operator()
	if _, _, err := r.hub.CreateLinkCode(ctx, alice); !errors.Is(err, ErrOverQuota) {
		t.Errorf("a user with no quota making a code: %v", err)
	}
	made, code, err := r.hub.CreateLinkCode(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^egzo-link-[a-z2-7]{26}$`).MatchString(code) || time.Until(made.Expires) > 15*time.Minute || time.Until(made.Expires) < 14*time.Minute {
		t.Errorf("code = %q, %+v", code, made)
	}
	if err := r.hub.CancelLinkCode(ctx, alice, made.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user cancelling somebody else's: %v", err)
	}
	if got, _ := r.hub.LinkCodes(ctx, alice); len(got) != 0 {
		t.Errorf("a user lists somebody else's: %+v", got)
	}
	listed, err := r.hub.LinkCodes(ctx, op)
	if err != nil || len(listed) != 1 || listed[0].ID != made.ID || listed[0].CreatorName != "admin" {
		t.Fatalf("codes = %+v, %v", listed, err)
	}
	if err := r.hub.CancelLinkCode(ctx, op, made.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.CancelLinkCode(ctx, op, made.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancelled twice: %v", err)
	}
	entries, _ := r.store.AuditOf(ctx, "", 20)
	actions := map[string]bool{}
	for _, e := range entries {
		if e.Allowed {
			actions[e.Action] = true
		}
		if strings.Contains(e.Detail, code) {
			t.Errorf("the code is in the audit log: %+v", e)
		}
	}
	if !actions["linkcode.create"] || !actions["linkcode.cancel"] {
		t.Errorf("audit = %+v", entries)
	}
}

func TestAnApprovedKeyIgnoresACodeAndDoesNotSpendIt(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	id := r.id("web")
	code := r.code()
	f.Drop()
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	r.dial(f, code)
	r.held(f)
	if verdict := last(f); verdict.State != hublink.StateApproved || verdict.Project != id {
		t.Errorf("the link was told %+v", verdict)
	}
	if got, _ := r.store.LinkCodes(ctx, "", time.Now()); len(got) != 1 {
		t.Errorf("the code was spent: %+v", got)
	}
}

func TestARevokedKeyIsRefusedKeepsItsCodeAndComesBackNewOnceForgotten(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	if err := r.hub.Revoke(ctx, op, id, "the laptop was stolen"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	code := r.code()
	if err := r.turnedAway(f, code); err != nil {
		t.Errorf("Link ended with %v", err)
	}
	if verdict := last(f); verdict.State != hublink.StateRevoked || verdict.Message != "the laptop was stolen" {
		t.Errorf("the link was told %+v", verdict)
	}
	if got, _ := r.store.LinkCodes(ctx, "", time.Now()); len(got) != 1 {
		t.Errorf("the code was spent: %+v", got)
	}
	if err := r.hub.Forget(ctx, op, id); err != nil {
		t.Fatal(err)
	}
	// its key is unknown again: it needs a code like any other, and it has nothing of what it had
	if err := r.turnedAway(f, ""); !errors.Is(err, ErrUnregistered) {
		t.Errorf("a forgotten key without a code: %v", err)
	}
	r.dial(f, code)
	r.held(f)
	again, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil || again.ID == id || again.State != hublink.StateApproved {
		t.Errorf("after forgetting: %+v, %v", again, err)
	}
	if granted, _ := r.store.GrantsOf(ctx, r.login("alice").ID); len(granted) != 0 {
		t.Errorf("grants = %+v", granted)
	}
}

func TestOfTwoLinksWithOneCodeExactlyOneRegisters(t *testing.T) {
	r := newRig(t)
	code := r.code()
	const contenders = 8
	ended := make([]<-chan error, contenders)
	for i := range ended {
		ended[i] = r.dial(fake.New("web", "laptop", "coder"), code)
	}
	failed := 0
	for _, e := range ended {
		select {
		case err := <-e:
			if !errors.Is(err, ErrBadCode) {
				t.Errorf("a loser ended with %v", err)
			}
			failed++
		case <-time.After(300 * time.Millisecond): // the winner is held
		}
	}
	regs, _ := r.store.Registrations(ctx)
	if failed != contenders-1 || len(regs) != 1 {
		t.Errorf("%d of %d failed, %d registered", failed, contenders, len(regs))
	}
}

func TestTwoConnectionsOfOneNewKeySpendOneCode(t *testing.T) {
	r := newRig(t)
	f := fake.New("web", "laptop", "coder")
	first, second := r.code(), r.code()
	r.dial(f, first)
	r.held(f)
	reg, err := r.store.RedeemLinkCode(ctx, second, store.Registration{Key: f.Hello().Key, Name: "web"}, time.Now())
	if !errors.Is(err, store.ErrKeyTaken) {
		t.Fatalf("a key redeemed twice: %+v, %v", reg, err)
	}
	if got, _ := r.store.LinkCodes(ctx, "", time.Now()); len(got) != 1 {
		t.Errorf("the code of the loser was spent: %+v", got)
	}
}

func TestAProjectRevokedAsItsLinkArrivesEndsUpRevoked(t *testing.T) {
	// the Revoke that comes after Link read the project, and before it published the connection: it finds nobody to tell
	r := newRig(t)
	f := r.link("web", "coder")
	id := r.id("web")
	f.Drop()
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	if err := r.hub.Revoke(ctx, r.operator(), id, "stolen"); err != nil {
		t.Fatal(err)
	}
	conn := f.Connect()
	ended := make(chan error, 1)
	go func() { ended <- r.hub.hold(ctx, id, f.Hello(), conn) }()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("a revoked project is held")
	}
	if verdict := last(f); verdict.State != hublink.StateRevoked || verdict.Message != "stolen" {
		t.Errorf("the link was told %+v", verdict)
	}
	if f.Running() {
		t.Error("a revoked project reached Run")
	}
	if r.hub.connected(id) != nil {
		t.Error("a revoked project stayed published")
	}
}

func TestTheOwnerStaysOwnerWhenTheyStopBeingAnOperator(t *testing.T) {
	r := newRig(t)
	op, alice := r.operator(), r.login("alice")
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	_, code, err := r.hub.CreateLinkCode(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	f := fake.New("web", "laptop", "coder")
	r.dial(f, code)
	r.held(f)
	eventually(t, "the link to hear it", f.Served)
	reg, _ := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err := r.hub.SetOperator(ctx, op, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	view, err := r.hub.Project(ctx, alice, reg.ID)
	if err != nil || view.Role != rbac.Owner {
		t.Errorf("the owner's view = %+v, %v", view, err)
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
	case <-f.Closed():
	case <-time.After(3 * time.Second):
		t.Fatal("the connection of a revoked project stayed open")
	}
	select {
	case err := <-ended:
		if !errors.As(err, new(OfflineError)) {
			t.Errorf("the terminal ended with %v: it looks like a session that ended by itself", err)
		}
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
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	r.turnedAway(f, "")
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

func TestWhatTheConnectionCutIsOfflineEvenIfItSaysNothingWentWrong(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	id := r.id("web")
	c := r.hub.connected(id)

	// the caller leaving is the caller's own doing: a person who closed their terminal
	leaving, leave := context.WithCancel(ctx)
	err := r.hub.call(leaving, id, func(ctx context.Context, _ links.Engine) error {
		leave()
		<-ctx.Done()
		return nil // what a terminal says when its context ends
	})
	if err != nil {
		t.Errorf("a caller that left: %v", err)
	}

	err = r.hub.call(ctx, id, func(ctx context.Context, _ links.Engine) error {
		c.end() // a link that dropped, or a revocation
		<-ctx.Done()
		return nil
	})
	if !errors.As(err, new(OfflineError)) {
		t.Errorf("a terminal the connection took with it ended with %v: it looks like a session that ended by itself", err)
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
	select {
	case <-f.Closed():
	default:
		t.Error("the connection stayed open after the answer")
	}
	eventually(t, "the connection to end", func() bool { return r.hub.connected(id) == nil })
	f.PushProject(links.ProjectUpdate{Project: links.Project{Name: "web", Up: true}}) // a feed that had a line on its way
	r.hub.mu.RLock()
	_, held := r.hub.held[id]
	r.hub.mu.RUnlock()
	if held {
		t.Error("what the project last pushed is still held")
	}
	if err := r.turnedAway(f, ""); !errors.Is(err, ErrUnregistered) {
		t.Errorf("it came back without a code: %v", err)
	}
	r.dial(f, r.code())
	r.held(f)
	again, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
	if err != nil || again.ID == id {
		t.Errorf("registered again: %+v, %v", again, err)
	}
}

func TestARevokedLinkCannotEraseItsOwnRevocation(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	if err := r.hub.Revoke(ctx, op, id, "stolen"); err != nil {
		t.Fatal(err)
	}
	// an Unlink that was on its way when the revocation came
	if verdict := f.Unlink(); verdict.State != hublink.StateRevoked || verdict.Message != "stolen" {
		t.Errorf("the link was told %+v", verdict)
	}
	reg, err := r.store.Registration(ctx, id)
	if err != nil || reg.State != hublink.StateRevoked || reg.Message != "stolen" {
		t.Fatalf("the revocation = %+v, %v", reg, err)
	}
	entries, _ := r.store.AuditOf(ctx, id, 10)
	for _, e := range entries {
		if e.Action == "project.unlink" {
			t.Errorf("an unlink that did nothing is in the log: %+v", e)
		}
	}
}

func TestAConnectionThatComesBackAsTheOldOneEndsStaysOnline(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	for range 100 {
		old, next := r.hub.connected(id), f.Again()
		r.dial(next, "")
		old.link.Close()
		r.held(next)
		eventually(t, "the feeds", next.Served)
		eventually(t, "the old connection to end", func() bool { return old.gone() })
		f = next
	}
	eventually(t, "what ended to settle", func() bool { return r.hub.connected(id) != nil })
	time.Sleep(50 * time.Millisecond)
	if reg, _ := r.store.Registration(ctx, id); !reg.Online() {
		t.Errorf("a project with a connection up is offline since %v", reg.Offline)
	}
	if view, err := r.hub.Project(ctx, op, id); err != nil || view.Feed.State == FeedOffline {
		t.Errorf("view = %+v, %v", view.Feed, err)
	}
}

func TestLogsOfAProjectThatIsOfflineSayOfflineAndARealDenialStaysADenial(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id, alice := r.operator(), r.id("web"), r.login("alice")
	if err := r.hub.SetRole(ctx, op, id, alice.ID, rbac.User); err != nil {
		t.Fatal(err)
	}
	f.SetAgent(agent("hers", rbac.Actor(alice.ID)))
	f.SetAgent(agent("theirs", "operator"))
	if _, err := r.hub.Logs(ctx, alice, id, "theirs", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("logs of someone else's agent: %v", err)
	}
	if _, err := r.hub.Logs(ctx, alice, id, "control", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("logs of a sidecar: %v", err)
	}
	if _, err := r.hub.Logs(ctx, alice, id, "hers", 10); err != nil {
		t.Errorf("logs of her own agent: %v", err)
	}
	f.Drop()
	eventually(t, "the hub to notice", func() bool { return r.hub.connected(id) == nil })
	if _, err := r.hub.Logs(ctx, alice, id, "hers", 10); !errors.As(err, new(OfflineError)) {
		t.Errorf("logs of her own agent, offline: %v", err)
	}
}

func TestATaskAsksTheLinkNothingAfterItsVerb(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	op, id := r.operator(), r.id("web")
	reads := f.ProjectReads()
	task, err := r.hub.StartSpawn(ctx, op, id, "coder", "fix-bug")
	if err != nil {
		t.Fatal(err)
	}
	r.hub.WaitForTasks()
	if got, _ := r.hub.TaskOf(ctx, op, id, task.ID); got.State != store.TaskDone {
		t.Fatalf("task = %+v", got)
	}
	if f.ProjectReads() != reads {
		t.Errorf("a finished task read the project %d times: a link that hangs on that keeps the task busy", f.ProjectReads()-reads)
	}
	if a, _ := r.agentIn(op, id, "fix-bug"); a.State != "running" {
		t.Errorf("what the page shows after the task comes from the feed: %+v", a)
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
	if err := r.hub.Link(ctx, bad.Hello(), "", bad.Connect()); !errors.Is(err, ErrBadHello) {
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
