package hub

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var ctx = context.Background()

type rig struct {
	t     *testing.T
	hub   *Hub
	store *store.Store
	fakes map[string]*fake.Engine // the projects that linked, by name
	op    store.User              // the operator, once they signed in: a password takes long to check
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, store: s, fakes: map[string]*fake.Engine{}}
	r.hub = New(s, nil, flushing)
	if _, err := r.hub.SetupSignIn(ctx, SignInOptions{LocalLogin: true, AdminPassword: "admin-password", Issuer: testIssuer}); err != nil {
		t.Fatal(err)
	}
	return r
}

// dial brings a project's link to the hub on a connection of its own, and returns what Link ends with.
func (r *rig) dial(f *fake.Engine, code string) <-chan error {
	ended := make(chan error, 1)
	conn := f.Connect()
	go func() { ended <- r.hub.Link(ctx, f.Hello(), code, conn) }()
	r.t.Cleanup(f.Drop)
	return ended
}

// connect brings a registered project's link to the hub, and waits until the hub holds it.
func (r *rig) connect(f *fake.Engine) {
	r.t.Helper()
	r.dial(f, "")
	r.held(f)
}

func (r *rig) held(f *fake.Engine) {
	r.t.Helper()
	eventually(r.t, "the link to be held", func() bool {
		reg, err := r.store.RegistrationByKey(ctx, f.Hello().Key)
		return err == nil && r.hub.connected(reg.ID) != nil && len(f.Verdicts()) > 0 && f.Running()
	})
}

// turnedAway is a link that dials the hub with a code and is told something other than approved. It returns the error Link ended with.
func (r *rig) turnedAway(f *fake.Engine, code string) error {
	r.t.Helper()
	before := len(f.Verdicts())
	select {
	case err := <-r.dial(f, code):
		if len(f.Verdicts()) == before {
			r.t.Fatal("the link was told nothing")
		}
		select {
		case <-f.Closed():
		default:
			r.t.Error("the connection stayed open")
		}
		return err
	case <-time.After(3 * time.Second):
		r.t.Fatal("the hub held a link it should have turned away")
		return nil
	}
}

// code makes a link code, as the operator.
func (r *rig) code() string {
	r.t.Helper()
	_, code, err := r.hub.CreateLinkCode(ctx, r.operator())
	if err != nil {
		r.t.Fatal(err)
	}
	return code
}

// link makes a project that registered with a code of the operator's.
func (r *rig) link(name string, templates ...string) *fake.Engine {
	r.t.Helper()
	return r.adopt(fake.New(name, "laptop", templates...))
}

// adopt registers a project a test has prepared.
func (r *rig) adopt(f *fake.Engine) *fake.Engine {
	r.t.Helper()
	r.dial(f, r.code())
	r.held(f)
	eventually(r.t, "the link to hear it", f.Served)
	r.fakes[f.Hello().Project] = f
	return f
}

// id is the id of a project that linked.
func (r *rig) id(name string) string {
	r.t.Helper()
	reg, err := r.store.RegistrationByKey(ctx, r.fakes[name].Hello().Key)
	if err != nil {
		r.t.Fatal(err)
	}
	return reg.ID
}

const testIssuer = "https://idp.example.com"

// login signs in through single sign-on as sub, whose username is sub too.
func (r *rig) login(sub string) store.User {
	r.t.Helper()
	u, _, _, err := r.hub.IdentityLogin(ctx, testIssuer, sub, store.Profile{Email: sub + "@example.com", Name: sub, PreferredUsername: sub}, r.invited())
	if err != nil {
		r.t.Fatal(err)
	}
	return u
}

// operator is the starter account, an operator.
// invited is a code that lets one new person in, with nothing else.
func (r *rig) invited() string {
	r.t.Helper()
	_, code, err := r.hub.InviteToHub(ctx, r.operator(), false, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return code
}

func (r *rig) operator() store.User {
	r.t.Helper()
	if r.op.ID == "" {
		u, err := r.hub.PasswordLogin(ctx, "admin", "admin-password")
		if err != nil {
			r.t.Fatal(err)
		}
		r.op = u
	}
	return r.op
}

func agent(name, actor string) links.Agent {
	return links.Agent{Name: name, Template: "coder", State: "running", Activity: "idle", Actor: actor, Created: time.Now()}
}

func (r *rig) role(user store.User, project string) rbac.Role {
	role, err := r.store.Role(ctx, project, user.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return role
}

func names(refs []ProjectRef) string {
	var out []string
	for _, ref := range refs {
		out = append(out, ref.Host+":"+ref.Project.Name)
	}
	return strings.Join(out, ",")
}

func TestAnOperatorOverseesEveryProjectWithoutAGrant(t *testing.T) {
	r := newRig(t)
	r.link("web")
	alice := r.login("alice")
	second := fake.New("data", "lab")
	r.adopt(second)
	op := r.operator()
	if !op.IsOperator {
		t.Fatal("the starter account is not an operator")
	}
	if alice.IsOperator {
		t.Fatal("a new user is an operator")
	}
	// what the other operator connected is theirs: this one only oversees it
	other := r.login("bob")
	if err := r.hub.SetOperator(ctx, op, other.ID, true); err != nil {
		t.Fatal(err)
	}
	other, _ = r.store.User(ctx, other.ID)
	refs, _ := r.hub.Projects(ctx, other)
	if names(refs) != "lab:data,laptop:web" {
		t.Errorf("projects = %s", names(refs))
	}
	for _, ref := range refs {
		if ref.Role != rbac.Overseer {
			t.Errorf("%s: role %q", ref.Project.Name, ref.Role)
		}
	}
	view, err := r.hub.Project(ctx, other, r.id("data"))
	if err != nil || view.Role != rbac.Overseer || view.CanManage || view.CanSpawn || view.CanUnlink || !view.CanMembers {
		t.Errorf("project view = %+v, %v", view, err)
	}
}

func TestAnOperatorGoesNoFurtherThanTheStateWithoutARole(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "user:someone"))
	other := r.login("bob")
	if err := r.hub.SetOperator(ctx, r.operator(), other.ID, true); err != nil {
		t.Fatal(err)
	}
	other, _ = r.store.User(ctx, other.ID)
	id := r.id("web")
	view, err := r.hub.Project(ctx, other, id)
	if err != nil || len(view.Agents) != 1 || view.Agents[0].CanOperate {
		t.Fatalf("the project shows %+v, %v", view.Agents, err)
	}
	refused := map[string]func() error{
		"logs":    func() error { _, err := r.hub.Logs(ctx, other, id, "coder-1", 10); return err },
		"events":  func() error { _, err := r.hub.Events(ctx, other, id, "", 10); return err },
		"spec":    func() error { _, err := r.hub.Spec(ctx, other, id); return err },
		"tasks":   func() error { _, err := r.hub.Tasks(ctx, other, id, "", 10); return err },
		"network": func() error { _, err := r.hub.Network(ctx, other, id, ""); return err },
		"audit":   func() error { _, err := r.hub.Audit(ctx, other, id, 10); return err },
		"attach":  func() error { _, err := r.hub.Attach(ctx, other, id, "coder-1", false); return err },
		"stop":    func() error { return r.hub.Operate(ctx, other, "stop", id, "coder-1") },
		"spawn":   func() error { return r.hub.Spawn(ctx, other, id, "coder", "coder-2") },
		"unlink":  func() error { return r.hub.Unlink(ctx, other, id) },
	}
	for name, try := range refused {
		if err := try(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s without a role: %v", name, err)
		}
	}
	// the role they give themselves is what opens it, and it is on the record and on the Members tab
	if err := r.hub.SetRole(ctx, other, id, other.ID, rbac.Admin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Logs(ctx, other, id, "coder-1", 10); err != nil {
		t.Errorf("logs with a role: %v", err)
	}
	members, _ := r.hub.Members(ctx, r.operator(), id)
	var listed bool
	for _, m := range members {
		listed = listed || m.User.ID == other.ID && m.Role == rbac.Admin
	}
	if !listed {
		t.Errorf("the operator's role is not listed: %+v", members)
	}
	entries, _ := r.store.AuditOf(ctx, id, 20)
	var audited bool
	for _, e := range entries {
		audited = audited || e.Action == "role.set" && e.Allowed && e.Actor == rbac.Actor(other.ID)
	}
	if !audited {
		t.Errorf("the grant was not audited: %+v", entries)
	}
}

func TestAViewerIsShownNothingOfTheProject(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	alice := r.login("alice")
	id := r.id("web")
	if err := r.hub.SetRole(ctx, r.operator(), id, alice.ID, rbac.Viewer); err != nil {
		t.Fatal(err)
	}
	if refs, _ := r.hub.Projects(ctx, alice); len(refs) != 0 {
		t.Errorf("a viewer lists %s", names(refs))
	}
	if _, err := r.hub.Project(ctx, alice, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("a viewer opened the project: %v", err)
	}
	if _, err := r.hub.Members(ctx, alice, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("a viewer read the members: %v", err)
	}
}

func TestAnOperatorReachesOnlyWhatExists(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	if _, err := r.hub.Project(ctx, op, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a project that is not there: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, "ghost", op.ID, rbac.None); !errors.Is(err, ErrNotFound) {
		t.Errorf("roles on a project that is not there: %v", err)
	}
	// a project that links later is held as soon as it is approved, and nobody else gets anything from that
	alice := r.login("alice")
	r.link("new")
	if _, err := r.hub.Project(ctx, op, r.id("new")); err != nil {
		t.Errorf("once approved: %v", err)
	}
	if refs, _ := r.hub.Projects(ctx, alice); len(refs) != 0 {
		t.Errorf("an approval gave alice %s", names(refs))
	}
	// a project somebody else owns is shown too, and the owner keeps their role
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.Owner)
	if view, err := r.hub.Project(ctx, op, r.id("web")); err != nil || view.Role != rbac.Owner {
		t.Errorf("a project with an owner: %+v %v", view, err)
	}
}

func TestAUserSeesOnlyTheProjectsTheyHaveARoleOn(t *testing.T) {
	r := newRig(t)
	r.link("web")
	r.link("secret")
	op := r.operator()
	alice := r.login("alice")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	refs, _ := r.hub.Projects(ctx, alice)
	if names(refs) != "laptop:web" || refs[0].Role != rbac.User {
		t.Errorf("alice sees %s", names(refs))
	}
	if _, err := r.hub.Project(ctx, alice, r.id("secret")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a project without a role is %v, want not found (its existence must not leak)", err)
	}
	if _, err := r.hub.Project(ctx, op, r.id("secret")); err != nil {
		t.Errorf("the operator: %v", err)
	}
	if _, err := r.hub.Project(ctx, alice, r.id("web")); err != nil {
		t.Errorf("alice on web: %v", err)
	}
	if _, err := r.hub.Project(ctx, alice, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestUsersOperateTheirOwnAgentsAndAdministratorsAllOfThem(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	op := r.operator()
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), carol.ID, rbac.Admin)

	if err := r.hub.Spawn(ctx, alice, r.id("web"), "coder", "coder-1"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if calls := r.fakes["web"].Calls(); calls[len(calls)-1] != "spawn coder-1 user:"+alice.ID {
		t.Errorf("the agent was not made on behalf of the user: %v", calls)
	}
	if err := r.hub.Operate(ctx, alice, "stop", r.id("web"), "coder-1"); err != nil {
		t.Errorf("alice stopping her own agent: %v", err)
	}
	if err := r.hub.Operate(ctx, bob, "start", r.id("web"), "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob starting alice's agent: %v", err)
	}
	if err := r.hub.Operate(ctx, carol, "start", r.id("web"), "coder-1"); err != nil {
		t.Errorf("an administrator: %v", err)
	}
	if err := r.hub.Operate(ctx, bob, "rm", r.id("web"), "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob removing: %v", err)
	}
	if err := r.hub.Operate(ctx, alice, "rm", r.id("web"), "coder-1"); err != nil {
		t.Errorf("alice removing her own: %v", err)
	}
	if err := r.hub.Operate(ctx, alice, "rm", r.id("web"), "coder-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a removed agent: %v", err)
	}
	if err := r.hub.Operate(ctx, op, "explode", r.id("web"), "coder-1"); err == nil {
		t.Error("an unknown verb was accepted")
	}

	entries, _ := r.store.AuditOf(ctx, r.id("web"), 50)
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

func TestAgentsSpawnedFromTheCLIAreForAdministrators(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	op := r.operator()
	alice := r.login("alice")
	bob := r.login("bob")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.Admin)
	if err := r.hub.Operate(ctx, alice, "stop", r.id("web"), "coder-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user stopping a CLI agent: %v", err)
	}
	if err := r.hub.Operate(ctx, bob, "stop", r.id("web"), "coder-1"); err != nil {
		t.Errorf("an administrator stopping a CLI agent: %v", err)
	}
	if err := r.hub.Operate(ctx, op, "start", r.id("web"), "coder-1"); err != nil {
		t.Errorf("the owner starting a CLI agent: %v", err)
	}
	view, _ := r.hub.Project(ctx, alice, r.id("web"))
	if view.Agents[0].CanOperate {
		t.Error("the page offers alice what she may not do")
	}
}

func TestSpawnNeedsARoleAndATemplate(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.operator()
	alice := r.login("alice")
	if err := r.hub.Spawn(ctx, alice, r.id("web"), "coder", "coder-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("spawn without a role: %v", err)
	}
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	if err := r.hub.Spawn(ctx, alice, r.id("web"), "ghost", "ghost-1"); err == nil {
		t.Error("spawned a template that does not exist")
	}
}

func TestOneHolderPerAgentAndAnyoneMayTakeOver(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	op := r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.Admin)
	r.store.SetRole(ctx, r.id("web"), bob.ID, rbac.User)
	stranger := r.login("eve")

	first, err := r.hub.Attach(ctx, op, r.id("web"), "coder-1", false)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	var inUse *InUseError
	if _, err := r.hub.Attach(ctx, alice, r.id("web"), "coder-1", false); !errors.As(err, &inUse) || inUse.Holder != "admin" {
		t.Errorf("second attach: %v", err)
	}
	if _, err := r.hub.Attach(ctx, bob, r.id("web"), "coder-1", false); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user who may not operate the agent attached: %v", err)
	}
	if _, err := r.hub.Attach(ctx, stranger, r.id("web"), "coder-1", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("attach without a role: %v", err)
	}
	if got, _ := r.hub.Holder(ctx, bob, r.id("web"), "coder-1"); got != "admin" {
		t.Errorf("holder = %q", got)
	}

	second, err := r.hub.Attach(ctx, alice, r.id("web"), "coder-1", true)
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
	if got := r.hub.holder(r.id("web"), "coder-1"); got != "alice" {
		t.Errorf("holder after the old terminal closed = %q", got)
	}
	second.Close()
	if got := r.hub.holder(r.id("web"), "coder-1"); got != "" || len(r.hub.attached) != 0 {
		t.Errorf("holder after everybody left: %q", got)
	}
	again, err := r.hub.Attach(ctx, op, r.id("web"), "coder-1", false)
	if err != nil {
		t.Errorf("the seat was not freed: %v", err)
	}
	again.Close()
}

func TestATakenOverAttachmentEndsWithoutAnError(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	op := r.operator()
	a, _ := r.hub.Attach(ctx, op, r.id("web"), "coder-1", false)
	done := make(chan error, 1)
	in, _ := io.Pipe()
	go func() { done <- a.Run(ctx, links.Terminal{In: in, Out: io.Discard}) }()
	b, err := r.hub.Attach(ctx, op, r.id("web"), "coder-1", true)
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
	r := newRig(t)
	r.link("web")
	op := r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	if err := r.hub.SetRole(ctx, op, r.id("web"), alice.ID, rbac.Admin); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetRole(ctx, alice, r.id("web"), bob.ID, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an administrator granting: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, r.id("web"), "uGHOST", rbac.User); !errors.Is(err, ErrNotFound) {
		t.Errorf("granting to nobody: %v", err)
	}
	r.hub.SetRole(ctx, op, r.id("web"), alice.ID, rbac.Owner)
	r.hub.SetRole(ctx, op, r.id("web"), bob.ID, rbac.Owner)
	if err := r.hub.SetRole(ctx, alice, r.id("web"), alice.ID, rbac.None); !errors.Is(err, ErrForbidden) {
		t.Errorf("an owner removing themselves: %v", err)
	}
	if err := r.hub.SetRole(ctx, alice, r.id("web"), alice.ID, rbac.Admin); !errors.Is(err, ErrForbidden) {
		t.Errorf("an owner lowering themselves: %v", err)
	}
	if err := r.hub.SetRole(ctx, alice, r.id("web"), bob.ID, rbac.None); err != nil {
		t.Errorf("an owner removing another owner: %v", err)
	}
	if _, err := r.hub.Members(ctx, bob, r.id("web")); !errors.Is(err, ErrNotFound) {
		t.Errorf("members without a role: %v", err)
	}
	members, err := r.hub.Members(ctx, alice, r.id("web"))
	if err != nil || len(members) != 2 {
		t.Errorf("members = %+v %v: want the operator who approved it and alice", members, err)
	}
	candidates, _ := r.hub.Candidates(ctx, alice, r.id("web"))
	if len(candidates) != 1 || candidates[0].ID != bob.ID {
		t.Errorf("candidates = %+v", candidates)
	}
	if _, err := r.hub.Audit(ctx, bob, r.id("web"), 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("audit without a role: %v", err)
	}
}

func TestAProjectKeepsItsLastOwner(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	id := r.id("web")
	// the operator made the code, so they are its owner, and they may step down: but not leave it with nobody
	if err := r.hub.SetRole(ctx, op, id, op.ID, rbac.None); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner left: %v", err)
	}
	if err := r.hub.SetRole(ctx, op, id, op.ID, rbac.Admin); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner lowered themselves: %v", err)
	}
	alice := r.login("alice")
	if err := r.hub.SetRole(ctx, op, id, alice.ID, rbac.Owner); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetRole(ctx, op, id, op.ID, rbac.None); err != nil {
		t.Errorf("an operator stepping down: %v", err)
	}
	// alice, who is no operator, cannot leave at all
	bob := r.login("bob")
	r.hub.SetRole(ctx, alice, id, bob.ID, rbac.Owner)
	if err := r.hub.SetRole(ctx, alice, id, alice.ID, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an owner lowering themselves: %v", err)
	}
	if err := r.hub.SetRole(ctx, alice, id, bob.ID, rbac.User); err != nil {
		t.Errorf("an owner lowering another: %v", err)
	}
}

func TestAProjectWithoutAValidOwnerIsShownToOperatorsWhoGiveItOne(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	id := r.id("web")
	alice := r.login("alice")
	bob := r.login("bob")
	carol := r.login("carol")
	r.hub.SetRole(ctx, op, id, alice.ID, rbac.Owner)
	r.hub.SetRole(ctx, op, id, op.ID, rbac.None)
	if err := r.hub.SetOperator(ctx, op, carol.ID, true); err != nil {
		t.Fatal(err)
	}
	carol, _ = r.store.User(ctx, carol.ID)
	if err := r.hub.SetRole(ctx, carol, id, bob.ID, rbac.Owner); !errors.Is(err, ErrForbidden) {
		t.Errorf("an operator gave away a project that has an owner: %v", err)
	}
	if _, err := r.hub.SetDisabled(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	refs, _ := r.hub.Projects(ctx, carol)
	if len(refs) != 1 || !refs[0].Ownerless {
		t.Fatalf("the project shows as %+v", refs)
	}
	views, _ := r.hub.Links(ctx, carol)
	if len(views) != 1 || len(views[0].Owners) != 0 {
		t.Fatalf("the links show %+v", views)
	}
	if reg, _ := r.store.Registration(ctx, id); !reg.Online() {
		t.Fatal("the project left with its owner")
	}
	if err := r.hub.SetRole(ctx, carol, id, bob.ID, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an ownerless project got something else than an owner: %v", err)
	}
	if err := r.hub.SetRole(ctx, carol, id, bob.ID, rbac.Owner); err != nil {
		t.Fatal(err)
	}
	if refs, _ := r.hub.Projects(ctx, carol); refs[0].Ownerless {
		t.Error("it stays ownerless with an owner")
	}
	// the disabled owner can go now
	if err := r.hub.SetRole(ctx, bob, id, alice.ID, rbac.None); err != nil {
		t.Errorf("removing a disabled owner: %v", err)
	}
}

func TestAnOperatorKeepsOnlyWhatTheyGrantedThemselvesWhenTheyStopBeingOne(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	alice := r.login("alice")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	members, _ := r.hub.Members(ctx, op, r.id("web"))
	var rows int
	for _, m := range members {
		if m.User.ID == alice.ID {
			rows++
			if m.Role != rbac.User {
				t.Errorf("alice listed as %+v", m)
			}
		}
	}
	if rows != 1 {
		t.Errorf("alice appears %d times", rows)
	}
	view, _ := r.hub.Project(ctx, alice, r.id("web"))
	if view.Role != rbac.User {
		t.Errorf("an operator with a grant holds the project as %q", view.Role)
	}
	if err := r.hub.SetOperator(ctx, op, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	if view, _ := r.hub.Project(ctx, alice, r.id("web")); view.Role != rbac.User {
		t.Errorf("after the demotion alice is %q", view.Role)
	}
}

func TestOperatorsAreMadeAndUnmadeByOperatorsAndTheLastOneStays(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	alice, bob := r.login("alice"), r.login("bob")
	if err := r.hub.SetOperator(ctx, alice, bob.ID, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user making an operator: %v", err)
	}
	if err := r.hub.SetOperator(ctx, alice, alice.ID, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user making themselves an operator: %v", err)
	}
	if err := r.hub.SetOperator(ctx, op, "uGHOST", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("making nobody an operator: %v", err)
	}
	if err := r.hub.SetOperator(ctx, op, op.ID, false); !errors.Is(err, ErrLastOperator) {
		t.Errorf("the last operator demoting themselves: %v", err)
	}
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetOperator(ctx, op, alice.ID, true); err != nil {
		t.Errorf("making an operator again: %v", err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	if err := r.hub.SetOperator(ctx, alice, op.ID, false); err != nil {
		t.Errorf("an operator demoting another: %v", err)
	}
	if err := r.hub.SetOperator(ctx, alice, alice.ID, false); !errors.Is(err, ErrLastOperator) {
		t.Errorf("nobody can demote the last operator: %v", err)
	}
	op, _ = r.store.User(ctx, op.ID)
	if err := r.hub.SetOperator(ctx, op, alice.ID, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("a demoted operator demoting the last one: %v", err)
	}
	// nothing was left without an operator
	users, err := r.hub.Users(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	var operators int
	for _, u := range users {
		if u.IsOperator {
			operators++
		}
	}
	if operators != 1 {
		t.Errorf("operators = %d", operators)
	}
	if _, err := r.hub.Users(ctx, bob); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user listing users: %v", err)
	}
}

func TestOperatorChangesAreAuditedWithNoProjectAndAnUnchangedOneIsNot(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	alice := r.login("alice")
	r.hub.SetOperator(ctx, op, alice.ID, true)
	r.hub.SetOperator(ctx, op, alice.ID, true) // nothing to change
	r.hub.SetOperator(ctx, op, alice.ID, false)
	r.hub.SetOperator(ctx, op, op.ID, false) // the last one: refused
	entries, err := r.store.AuditOf(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		got = append(got, e.Action+":"+map[bool]string{true: "allowed", false: "refused"}[e.Allowed])
	}
	want := "user.signin:allowed,invite.create:allowed,user.create:allowed,invite.redeem:allowed,operator.grant:allowed,operator.revoke:allowed,operator.revoke:refused"
	if strings.Join(got, ",") != want {
		t.Errorf("audit = %s, want %s", strings.Join(got, ","), want)
	}
}

func TestAdministratorsReadTheAuditLogAndUsersDoNot(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	alice := r.login("alice")
	r.hub.SetRole(ctx, op, r.id("web"), alice.ID, rbac.User)
	if _, err := r.hub.Audit(ctx, alice, r.id("web"), 5); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user reading the audit log: %v", err)
	}
	entries, err := r.hub.Audit(ctx, op, r.id("web"), 5)
	if err != nil || len(entries) == 0 || entries[0].Action != "role.set" || !entries[0].Allowed {
		t.Errorf("entries = %+v %v", entries, err)
	}
}

func TestEventsLogsAndSpecNeedARole(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(agent("coder-1", "operator"))
	r.fakes["web"].AddEvent(links.Event{Seq: 1, Type: "status", Agent: "coder-1", Text: "parsing"})
	op := r.operator()
	alice := r.login("alice")
	if _, err := r.hub.Events(ctx, alice, r.id("web"), "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("events without a role: %v", err)
	}
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	if events, err := r.hub.Events(ctx, alice, r.id("web"), "", 10); err != nil || len(events) != 1 {
		t.Errorf("events = %+v %v", events, err)
	}
	if _, err := r.hub.Spec(ctx, alice, r.id("web")); err != nil {
		t.Errorf("spec: %v", err)
	}
	if _, err := r.hub.Logs(ctx, alice, r.id("web"), "coder-1", 50); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user reading the log of an agent that is not theirs: %v", err)
	}
	if out, err := r.hub.Logs(ctx, op, r.id("web"), "coder-1", 50); err != nil || !strings.Contains(out, "coder-1") {
		t.Errorf("logs = %q %v", out, err)
	}
}
