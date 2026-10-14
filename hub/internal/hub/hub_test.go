package hub

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var ctx = context.Background()

type rig struct {
	t     *testing.T
	hub   *Hub
	store *store.Store
	fakes map[string]*fake.Engine
	bad   map[string]error // engines whose connection test fails
}

func newRig(t *testing.T, engineNames ...string) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := secretbox.New("0123456789abcdef-secret")
	r := &rig{t: t, store: s, fakes: map[string]*fake.Engine{}, bad: map[string]error{}}
	registry := engines.NewRegistry(s, box, func(_ context.Context, cfg engines.Config) (engines.Engine, error) {
		if err := r.bad[cfg.Name]; err != nil {
			return nil, err
		}
		f, ok := r.fakes[cfg.Name]
		if !ok {
			f = fake.New()
			r.fakes[cfg.Name] = f
		}
		return f, nil
	})
	registry.SetProbe(func(context.Context, engines.Config) (string, error) { return "SHA256:probed", nil })
	r.hub = New(s, registry, nil)
	for _, name := range engineNames {
		r.fakes[name] = fake.New()
		if err := s.PutEngine(ctx, store.EngineRow{Name: name, Host: "unix:///" + name}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *rig) login(sub string) store.User {
	r.t.Helper()
	u, err := r.hub.Login(ctx, "issuer\n"+sub, sub+"@example.com", sub)
	if err != nil {
		r.t.Fatal(err)
	}
	return u
}

func (r *rig) scan() {
	r.t.Helper()
	if err := r.hub.Scan(ctx); err != nil {
		r.t.Fatal(err)
	}
}

func project(name string, templates ...string) engines.Project {
	return engines.Project{Name: name, Templates: templates}
}

func agent(name, actor string) engines.Agent {
	return engines.Agent{Name: name, Template: "coder", State: "running", Activity: "idle", Actor: actor, Created: time.Now()}
}

func (r *rig) role(user store.User, engine, project string) rbac.Role {
	role, err := r.store.Role(ctx, engine, project, user.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return role
}

func names(refs []ProjectRef) string {
	var out []string
	for _, ref := range refs {
		out = append(out, ref.Engine+":"+ref.Project.Name)
	}
	return strings.Join(out, ",")
}

func TestTheFirstLoginIsTheOperatorAndOwnsWhatWasAlreadyDiscovered(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["local"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("data"))
	r.scan() // nobody has logged in: nothing is claimed
	if owners, _ := r.store.HasOwner(ctx, "local", "web"); owners {
		t.Fatal("a project was claimed before anybody logged in")
	}
	op := r.login("root")
	if !op.IsOperator {
		t.Fatal("the first login is not the operator")
	}
	for _, p := range [][2]string{{"local", "web"}, {"lab", "web"}, {"lab", "data"}} {
		if got := r.role(op, p[0], p[1]); got != rbac.Owner {
			t.Errorf("operator on %s/%s = %q", p[0], p[1], got)
		}
	}
	refs, _, _ := r.hub.Projects(ctx, op)
	if names(refs) != "lab:data,lab:web,local:web" {
		t.Errorf("projects = %s", names(refs))
	}
}

func TestAProjectThatAppearsLaterIsClaimedByTheOperatorOnTheNextScan(t *testing.T) {
	r := newRig(t, "local")
	op := r.login("root")
	other := r.login("alice")
	r.fakes["local"].AddProject(project("web"))
	r.scan()
	if r.role(op, "local", "web") != rbac.Owner {
		t.Error("the operator did not claim the new project")
	}
	if r.role(other, "local", "web") != rbac.None {
		t.Error("another user got a role from a scan")
	}
	// once somebody owns it, it is not claimed again, even if the operator is removed from it
	r.store.SetRole(ctx, "local", "web", other.ID, rbac.Owner)
	r.store.SetRole(ctx, "local", "web", op.ID, rbac.None)
	r.scan()
	if r.role(op, "local", "web") != rbac.None {
		t.Error("a project with an owner was claimed")
	}
}

func TestAUserSeesOnlyTheProjectsTheyHaveARoleOn(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	r.fakes["local"].AddProject(project("secret"))
	op := r.login("root")
	alice := r.login("alice")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	refs, _, _ := r.hub.Projects(ctx, alice)
	if names(refs) != "local:web" || refs[0].Role != rbac.User {
		t.Errorf("alice sees %s", names(refs))
	}
	if _, err := r.hub.Project(ctx, alice, "local", "secret"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a project without a role is %v, want not found (its existence must not leak)", err)
	}
	if _, err := r.hub.Project(ctx, op, "local", "secret"); err != nil {
		t.Errorf("the operator: %v", err)
	}
	if _, err := r.hub.Project(ctx, alice, "local", "web"); err != nil {
		t.Errorf("alice on web: %v", err)
	}
	if _, err := r.hub.Project(ctx, alice, "nope", "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown engine: %v", err)
	}
}

func TestOneEngineDownKeepsTheOthersListedAndIsReported(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["local"].AddProject(project("web"))
	r.fakes["lab"].AddProject(project("data"))
	op := r.login("root")
	r.scan()
	r.fakes["lab"].SetDown(fake.ErrUnreachable)
	r.scan()
	refs, down, err := r.hub.Projects(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if names(refs) != "local:web" || len(down) != 1 || down[0].Name != "lab" || !strings.Contains(down[0].Err, "connection refused") {
		t.Errorf("refs=%s down=%+v", names(refs), down)
	}
	views, _ := r.hub.Engines(ctx)
	if len(views) != 2 || !views[0].Up && views[0].Name == "lab" {
		t.Logf("views = %+v", views)
	}
	// it comes back
	r.fakes["lab"].SetDown(nil)
	r.hub.registry.Forget("lab")
	r.scan()
	if refs, down, _ := r.hub.Projects(ctx, op); len(refs) != 2 || len(down) != 0 {
		t.Errorf("after recovery: %s, down %+v", names(refs), down)
	}
}

func TestUsersOperateTheirOwnAgentsAndAdministratorsAllOfThem(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.login("root")
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", carol.ID, rbac.Administrator)

	name, err := r.hub.Spawn(ctx, alice, "local", "web", "coder", "")
	if err != nil || name != "coder-1" {
		t.Fatalf("spawn: %q %v", name, err)
	}
	if calls := r.fakes["local"].Calls(); calls[len(calls)-1] != "spawn web/coder-1 user:"+alice.ID {
		t.Errorf("the agent was not made on behalf of the user: %v", calls)
	}
	if err := r.hub.Operate(ctx, alice, "stop", "local", "web", "coder-1"); err != nil {
		t.Errorf("alice stopping her own agent: %v", err)
	}
	if err := r.hub.Operate(ctx, bob, "start", "local", "web", "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob starting alice's agent: %v", err)
	}
	if err := r.hub.Operate(ctx, carol, "start", "local", "web", "coder-1"); err != nil {
		t.Errorf("an administrator: %v", err)
	}
	if err := r.hub.Operate(ctx, bob, "rm", "local", "web", "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob removing: %v", err)
	}
	if err := r.hub.Operate(ctx, alice, "rm", "local", "web", "coder-1"); err != nil {
		t.Errorf("alice removing her own: %v", err)
	}
	if err := r.hub.Operate(ctx, alice, "rm", "local", "web", "coder-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a removed agent: %v", err)
	}
	if err := r.hub.Operate(ctx, op, "explode", "local", "web", "coder-1"); err == nil {
		t.Error("an unknown verb was accepted")
	}

	entries, _ := r.store.AuditOf(ctx, "local", "web", 50)
	var refused int
	for _, e := range entries {
		if !e.Allowed {
			refused++
			if e.Actor != "user:"+bob.ID {
				t.Errorf("a refusal by %s", e.Actor)
			}
		}
	}
	if refused != 2 {
		t.Errorf("refusals audited = %d, want 2 (%+v)", refused, entries)
	}
}

func TestAgentsSpawnedFromTheCLIBelongToTheOperator(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	op := r.login("root")
	alice := r.login("alice")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", op.ID, rbac.User) // even the operator, as a plain user, keeps these
	if err := r.hub.Operate(ctx, alice, "stop", "local", "web", "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user stopping a CLI agent: %v", err)
	}
	if err := r.hub.Operate(ctx, op, "stop", "local", "web", "coder-1"); err != nil {
		t.Errorf("the operator stopping a CLI agent: %v", err)
	}
	view, _ := r.hub.Project(ctx, alice, "local", "web")
	if view.Agents[0].CanOperate {
		t.Error("the page offers alice what she may not do")
	}
}

func TestSpawnNeedsARoleAndATemplate(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.login("root")
	alice := r.login("alice")
	r.scan()
	if _, err := r.hub.Spawn(ctx, alice, "local", "web", "coder", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("spawn without a role: %v", err)
	}
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	if _, err := r.hub.Spawn(ctx, alice, "local", "web", "ghost", ""); err == nil {
		t.Error("spawned a template that does not exist")
	}
}

func TestOneHolderPerAgentAndAnyoneMayTakeOver(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	op := r.login("root")
	alice, bob := r.login("alice"), r.login("bob")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.Administrator)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)
	stranger := r.login("eve")

	first, err := r.hub.Attach(ctx, op, "local", "web", "coder-1", false)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	var inUse *InUseError
	if _, err := r.hub.Attach(ctx, alice, "local", "web", "coder-1", false); !errors.As(err, &inUse) || inUse.Holder != "root" {
		t.Errorf("second attach: %v", err)
	}
	if _, err := r.hub.Attach(ctx, bob, "local", "web", "coder-1", false); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user who may not operate the agent attached: %v", err)
	}
	if _, err := r.hub.Attach(ctx, stranger, "local", "web", "coder-1", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("attach without a role: %v", err)
	}
	if got, _ := r.hub.Holder(ctx, bob, "local", "web", "coder-1"); got != "root" {
		t.Errorf("holder = %q", got)
	}

	second, err := r.hub.Attach(ctx, alice, "local", "web", "coder-1", true)
	if err != nil {
		t.Fatalf("take over: %v", err)
	}
	select {
	case <-first.Taken():
	default:
		t.Error("the first terminal was not told it was taken over")
	}
	if first.TakenBy() != "alice" {
		t.Errorf("taken by %q", first.TakenBy())
	}
	first.Close() // the old terminal ending must not free the new holder's seat
	if got := r.hub.holder("local", "web", "coder-1"); got != "alice" {
		t.Errorf("holder after the old terminal closed = %q", got)
	}
	second.Close()
	if got := r.hub.holder("local", "web", "coder-1"); got != "" || len(r.hub.attached) != 0 {
		t.Errorf("holder after everybody left: %q", got)
	}
	again, err := r.hub.Attach(ctx, op, "local", "web", "coder-1", false)
	if err != nil {
		t.Errorf("the seat was not freed: %v", err)
	}
	again.Close()
}

func TestATakenOverAttachmentEndsWithoutAnError(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	op := r.login("root")
	r.scan()
	a, _ := r.hub.Attach(ctx, op, "local", "web", "coder-1", false)
	done := make(chan error, 1)
	in, _ := io.Pipe()
	go func() { done <- a.Run(ctx, engines.Terminal{In: in, Out: io.Discard}) }()
	b, err := r.hub.Attach(ctx, op, "local", "web", "coder-1", true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the taken-over terminal kept running")
	}
}

func TestRolesAreManagedByOwnersOnly(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	op := r.login("root")
	alice, bob := r.login("alice"), r.login("bob")
	r.scan()
	if err := r.hub.SetRole(ctx, op, "local", "web", alice.ID, rbac.Administrator); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetRole(ctx, alice, "local", "web", bob.ID, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an administrator granting: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, "local", "web", op.ID, rbac.None); !errors.Is(err, ErrForbidden) {
		t.Errorf("an owner removing themselves: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, "local", "web", op.ID, rbac.Administrator); !errors.Is(err, ErrForbidden) {
		t.Errorf("an owner lowering themselves: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, "local", "web", "uGHOST", rbac.User); !errors.Is(err, ErrNotFound) {
		t.Errorf("granting to nobody: %v", err)
	}
	r.hub.SetRole(ctx, op, "local", "web", alice.ID, rbac.Owner)
	if err := r.hub.SetRole(ctx, alice, "local", "web", op.ID, rbac.None); err != nil {
		t.Errorf("an owner removing another owner: %v", err)
	}
	if _, err := r.hub.Members(ctx, bob, "local", "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("members without a role: %v", err)
	}
	members, err := r.hub.Members(ctx, alice, "local", "web")
	if err != nil || len(members) != 1 || members[0].User.ID != alice.ID {
		t.Errorf("members = %+v %v", members, err)
	}
	candidates, _ := r.hub.Candidates(ctx, alice, "local", "web")
	if len(candidates) != 2 {
		t.Errorf("candidates = %+v", candidates)
	}
	if _, err := r.hub.Audit(ctx, bob, "local", "web", 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("audit without a role: %v", err)
	}
}

func TestAdministratorsReadTheAuditLogAndUsersDoNot(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	op := r.login("root")
	alice := r.login("alice")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	if _, err := r.hub.Audit(ctx, alice, "local", "web", 5); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user reading the audit log: %v", err)
	}
	entries, err := r.hub.Audit(ctx, op, "local", "web", 5)
	if err != nil || len(entries) == 0 || entries[len(entries)-1].Action != "project.claim" {
		t.Errorf("entries = %+v %v", entries, err)
	}
}

func TestOnlyTheOperatorManagesEngines(t *testing.T) {
	r := newRig(t)
	op := r.login("root")
	alice := r.login("alice")
	in := EngineInput{Name: "lab", Host: "unix:///lab.sock"}
	if err := r.hub.SaveEngine(ctx, alice, in); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user adding an engine: %v", err)
	}
	if err := r.hub.SaveEngine(ctx, op, in); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.RemoveEngine(ctx, alice, "lab"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user removing an engine: %v", err)
	}
	for _, bad := range []EngineInput{{Name: "Bad Name", Host: "unix:///x"}, {Name: "x", Host: "http://x"}, {Name: "x", Host: "unix://"}, {Name: "x", Host: "tcp://"}} {
		if err := r.hub.SaveEngine(ctx, op, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if err := r.hub.RemoveEngine(ctx, op, "lab"); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.RemoveEngine(ctx, op, "lab"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}

func TestAnEngineThatCannotBeReachedIsNotSaved(t *testing.T) {
	r := newRig(t)
	op := r.login("root")
	r.bad["lab"] = errors.New("dial tcp: connection refused")
	err := r.hub.SaveEngine(ctx, op, EngineInput{Name: "lab", Host: "tcp://lab:2376"})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
	if rows, _ := r.store.Engines(ctx); len(rows) != 0 {
		t.Errorf("an engine that cannot be reached was saved: %+v", rows)
	}
}

func TestAnSSHEngineNeedsItsHostKeyConfirmedAndCredentialsAreWriteOnly(t *testing.T) {
	r := newRig(t)
	op := r.login("root")
	in := EngineInput{Name: "lab", Host: "ssh://me@lab", SSHKey: "PRIVATE-SSH-KEY"}
	err := r.hub.SaveEngine(ctx, op, in)
	var pending *HostKeyPending
	if !errors.As(err, &pending) || pending.Fingerprint != "SHA256:probed" {
		t.Fatalf("err = %v, want the fingerprint to confirm", err)
	}
	if rows, _ := r.store.Engines(ctx); len(rows) != 0 {
		t.Fatal("saved before the host key was confirmed")
	}
	in.PinHostKey = pending.Fingerprint
	if err := r.hub.SaveEngine(ctx, op, in); err != nil {
		t.Fatal(err)
	}
	row, _ := r.store.Engine(ctx, "lab")
	if row.HostKey != "SHA256:probed" || strings.Contains(string(row.Credentials), "PRIVATE-SSH-KEY") {
		t.Errorf("row = %+v", row)
	}
	// an edit that leaves the credential blank keeps it, and keeps the pin while the host is the same
	if err := r.hub.SaveEngine(ctx, op, EngineInput{Name: "lab", Host: "ssh://me@lab"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	creds, _ := r.hub.registry.Open(mustEngine(t, r, "lab"))
	if string(creds.SSHKey) != "PRIVATE-SSH-KEY" {
		t.Errorf("the stored key was lost on an edit")
	}
	// a new host asks for the key again
	if err := r.hub.SaveEngine(ctx, op, EngineInput{Name: "lab", Host: "ssh://me@elsewhere"}); !errors.As(err, &pending) {
		t.Errorf("changing the host kept the pin: %v", err)
	}
	if err := r.hub.SaveEngine(ctx, op, EngineInput{Name: "k", Host: "ssh://me@k"}); err == nil || errors.As(err, &pending) {
		t.Errorf("an ssh engine without a key: %v", err)
	}
}

func mustEngine(t *testing.T, r *rig, name string) store.EngineRow {
	t.Helper()
	row, err := r.store.Engine(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestTheLocalEngineIsSeededOnlyWhenNoneExists(t *testing.T) {
	r := newRig(t)
	if err := r.hub.SeedEngine(ctx, "local", "unix:///var/run/docker.sock"); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SeedEngine(ctx, "other", "unix:///elsewhere"); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.store.Engines(ctx)
	if len(rows) != 1 || rows[0].Name != "local" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestRemovingAnEngineRemovesItsGrantsAndItsProjectsFromTheList(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["lab"].AddProject(project("data"))
	op := r.login("root")
	r.scan()
	if err := r.hub.RemoveEngine(ctx, op, "lab"); err != nil {
		t.Fatal(err)
	}
	refs, _, _ := r.hub.Projects(ctx, op)
	if len(refs) != 0 {
		t.Errorf("refs = %s", names(refs))
	}
	if r.role(op, "lab", "data") != rbac.None {
		t.Error("the grants outlived the engine")
	}
}

func TestEventsLogsAndSpecNeedARole(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", agent("coder-1", "operator"))
	r.fakes["local"].AddEvent("web", engines.Event{Seq: 1, Type: "status", Agent: "coder-1", Text: "parsing"})
	op := r.login("root")
	alice := r.login("alice")
	r.scan()
	if _, err := r.hub.Events(ctx, alice, "local", "web", "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("events without a role: %v", err)
	}
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	if events, err := r.hub.Events(ctx, alice, "local", "web", "", 10); err != nil || len(events) != 1 {
		t.Errorf("events = %+v %v", events, err)
	}
	if _, err := r.hub.Spec(ctx, alice, "local", "web"); err != nil {
		t.Errorf("spec: %v", err)
	}
	if _, err := r.hub.Logs(ctx, alice, "local", "web", "coder-1", 50); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user reading the log of an agent that is not theirs: %v", err)
	}
	if out, err := r.hub.Logs(ctx, op, "local", "web", "coder-1", 50); err != nil || !strings.Contains(out, "coder-1") {
		t.Errorf("logs = %q %v", out, err)
	}
}
