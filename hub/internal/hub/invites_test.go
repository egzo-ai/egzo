package hub

import (
	"errors"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

func audited(r *rig, project, action string) []store.AuditEntry {
	entries, _ := r.store.AuditOf(ctx, project, 100)
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestSingleSignOnIsRefusedWithoutAnInviteAndLeavesNobody(t *testing.T) {
	r := newRig(t)
	if _, _, _, err := r.hub.IdentityLogin(ctx, testIssuer, "stranger", store.Profile{}, ""); !errors.Is(err, ErrNoInvite) {
		t.Errorf("without an invite: %v", err)
	}
	if _, _, _, err := r.hub.IdentityLogin(ctx, testIssuer, "stranger", store.Profile{}, "egzo-invite-nonsense"); !errors.Is(err, ErrBadInvite) {
		t.Errorf("with a bad one: %v", err)
	}
	if users, _ := r.store.Users(ctx); len(users) != 1 {
		t.Errorf("users = %+v", users)
	}
}

func TestAnOwnerInvitesToARoleOnTheirProject(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	op := r.operator()
	made, code, err := r.hub.InviteToProject(ctx, op, id, rbac.Admin)
	if err != nil {
		t.Fatal(err)
	}
	view, err := r.hub.PeekInvite(ctx, code)
	if err != nil || view.ProjectName != "web" || view.Role != rbac.Admin || view.CreatorName != "admin" {
		t.Errorf("peek = %+v %v", view, err)
	}
	user, _, unspent, err := r.hub.IdentityLogin(ctx, testIssuer, "alice", store.Profile{PreferredUsername: "alice"}, code)
	if err != nil || unspent {
		t.Fatal(err, unspent)
	}
	if role := r.role(user, id); role != rbac.Admin {
		t.Errorf("role = %q", role)
	}
	if got := audited(r, id, "invite.redeem"); len(got) != 1 || got[0].Actor != rbac.Actor(user.ID) || !strings.Contains(got[0].Detail, made.ID) {
		t.Errorf("audit = %+v", got)
	}
	if got := audited(r, id, "invite.create"); len(got) != 1 {
		t.Errorf("audit = %+v", got)
	}
}

func TestWhoMayInvite(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	admin, other := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, id, admin.ID, rbac.Admin)
	if _, _, err := r.hub.InviteToProject(ctx, admin, id, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an administrator inviting: %v", err)
	}
	if _, _, err := r.hub.InviteToProject(ctx, other, id, rbac.User); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger to the project inviting: %v", err)
	}
	if _, _, err := r.hub.InviteToProject(ctx, r.operator(), id, "boss"); !errors.Is(err, ErrBadRole) {
		t.Errorf("what is not a role: %v", err)
	}
	if _, _, err := r.hub.InviteToHub(ctx, admin, true, 0); !errors.Is(err, ErrForbidden) {
		t.Errorf("a person making an operator: %v", err)
	}
	if _, _, err := r.hub.InviteToHub(ctx, r.operator(), false, -1); !errors.Is(err, ErrBadQuota) {
		t.Errorf("a quota under zero: %v", err)
	}
	// an operator with no role on the project is not its owner: they go through the role
	second := r.login("carol")
	r.hub.SetOperator(ctx, r.operator(), second.ID, true)
	second, _ = r.store.User(ctx, second.ID)
	if _, _, err := r.hub.InviteToProject(ctx, second, id, rbac.User); !errors.Is(err, ErrForbidden) {
		t.Errorf("an operator inviting without a role: %v", err)
	}
}

func TestAnOperatorInvitesToTheFlags(t *testing.T) {
	r := newRig(t)
	_, code, err := r.hub.InviteToHub(ctx, r.operator(), true, 3)
	if err != nil {
		t.Fatal(err)
	}
	user, _, _, err := r.hub.IdentityLogin(ctx, testIssuer, "alice", store.Profile{PreferredUsername: "alice"}, code)
	if err != nil {
		t.Fatal(err)
	}
	if user, _ = r.store.User(ctx, user.ID); !user.IsOperator || user.ProjectQuota != 3 {
		t.Errorf("flags = %+v", user)
	}
}

func TestWhoListsAndRevokesInvites(t *testing.T) {
	r := newRig(t)
	r.link("web")
	id := r.id("web")
	op := r.operator()
	owner, other := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, id, owner.ID, rbac.Owner)
	onProject, _, _ := r.hub.InviteToProject(ctx, owner, id, rbac.User)
	onHub, _, _ := r.hub.InviteToHub(ctx, op, false, 0)
	if list, err := r.hub.ProjectInvites(ctx, owner, id); err != nil || len(list) != 1 {
		t.Errorf("the owner's list: %+v %v", list, err)
	}
	if _, err := r.hub.Invites(ctx, owner); !errors.Is(err, ErrForbidden) {
		t.Errorf("a person listing every invite: %v", err)
	}
	if list, _ := r.hub.Invites(ctx, op); len(list) != 2 {
		t.Errorf("the operators' list: %+v", list)
	}
	if err := r.hub.RevokeInvite(ctx, other, onProject.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger revoking: %v", err)
	}
	if err := r.hub.RevokeInvite(ctx, owner, onHub.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("an owner revoking a hub invite: %v", err)
	}
	if err := r.hub.RevokeInvite(ctx, owner, onProject.ID); err != nil {
		t.Errorf("the owner revoking theirs: %v", err)
	}
	if err := r.hub.RevokeInvite(ctx, op, onHub.ID); err != nil {
		t.Errorf("an operator revoking: %v", err)
	}
}

func TestAPersonAcceptsAnInviteWhileSignedIn(t *testing.T) {
	r := newRig(t)
	r.link("web")
	alice := r.login("alice")
	_, code, _ := r.hub.InviteToProject(ctx, r.operator(), r.id("web"), rbac.User)
	if _, err := r.hub.AcceptInvite(ctx, alice, code); err != nil {
		t.Fatal(err)
	}
	if refs, _ := r.hub.Projects(ctx, alice); len(refs) != 1 {
		t.Errorf("alice sees %s", names(refs))
	}
	if _, err := r.hub.AcceptInvite(ctx, r.login("bob"), code); !errors.Is(err, ErrBadInvite) {
		t.Errorf("spent twice: %v", err)
	}
}

func TestAKnownPersonWhoSignsInWithAnInviteIsToldItIsUnspent(t *testing.T) {
	r := newRig(t)
	r.login("alice")
	_, _, unspent, err := r.hub.IdentityLogin(ctx, testIssuer, "alice", store.Profile{}, r.invited())
	if err != nil || !unspent {
		t.Errorf("unspent = %v, %v", unspent, err)
	}
}

func TestALocalAccountComesFromAnInvite(t *testing.T) {
	r := newRig(t)
	r.link("web")
	const password = "correct horse battery"
	if _, err := r.hub.RegisterLocal(ctx, "egzo-invite-nonsense", "sam", password); !errors.Is(err, ErrBadInvite) {
		t.Errorf("a bad code: %v", err)
	}
	_, code, _ := r.hub.InviteToProject(ctx, r.operator(), r.id("web"), rbac.User)
	if _, err := r.hub.RegisterLocal(ctx, code, "Not A Name", password); !errors.Is(err, ErrBadUsername) {
		t.Errorf("a bad name: %v", err)
	}
	if _, err := r.hub.RegisterLocal(ctx, code, "operator", password); !errors.Is(err, ErrBadUsername) {
		t.Errorf("a reserved name: %v", err)
	}
	if _, err := r.hub.RegisterLocal(ctx, code, "sam", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("a short password: %v", err)
	}
	if _, err := r.hub.RegisterLocal(ctx, code, "admin2", strings.Repeat("x", maxPassword+1)); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("a huge password: %v", err)
	}
	user, err := r.hub.RegisterLocal(ctx, code, "sam", password)
	if err != nil {
		t.Fatal(err)
	}
	if r.role(user, r.id("web")) != rbac.User {
		t.Errorf("role = %q", r.role(user, r.id("web")))
	}
	if signed, err := r.hub.PasswordLogin(ctx, "sam", password); err != nil || signed.ID != user.ID {
		t.Errorf("signing in = %+v %v", signed, err)
	}
}

func TestLocalAccountsAreOffWithLocalLogin(t *testing.T) {
	r := newRig(t)
	code := r.invited()
	if _, err := r.hub.SetupSignIn(ctx, SignInOptions{LocalLogin: false, Issuer: testIssuer}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.RegisterLocal(ctx, code, "sam", "correct horse battery"); !errors.Is(err, ErrNoLocalLogin) {
		t.Errorf("registering: %v", err)
	}
}

func TestAQuotaGatesMakingACodeAndUsingIt(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	alice := r.login("alice")
	if _, _, err := r.hub.CreateLinkCode(ctx, alice); !errors.Is(err, ErrOverQuota) {
		t.Errorf("a quota of 0: %v", err)
	}
	if err := r.hub.SetProjectQuota(ctx, alice, alice.ID, 5); !errors.Is(err, ErrForbidden) {
		t.Errorf("setting one's own quota: %v", err)
	}
	if err := r.hub.SetProjectQuota(ctx, op, alice.ID, -1); !errors.Is(err, ErrBadQuota) {
		t.Errorf("a negative quota: %v", err)
	}
	if err := r.hub.SetProjectQuota(ctx, op, alice.ID, 1); err != nil {
		t.Fatal(err)
	}
	alice, _ = r.store.User(ctx, alice.ID)
	_, first, err := r.hub.CreateLinkCode(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := r.hub.CreateLinkCode(ctx, alice)
	if err != nil {
		t.Fatal(err) // two codes are not two projects: the second is refused when it is spent
	}
	f := fake.New("web", "laptop")
	r.dial(f, first)
	r.held(f)
	eventually(t, "the link to hear it", f.Served)
	r.fakes["web"] = f
	if _, _, err := r.hub.CreateLinkCode(ctx, alice); !errors.Is(err, ErrOverQuota) {
		t.Errorf("over the quota: %v", err)
	}
	if err := r.turnedAway(fake.New("api", "laptop"), second); !errors.Is(err, ErrBadCode) {
		t.Errorf("a code over the quota: %v", err)
	}
	entries, _ := r.store.AuditOf(ctx, "", 50)
	var refused bool
	for _, e := range entries {
		refused = refused || e.Action == "project.register" && !e.Allowed && strings.Contains(e.Detail, "quota")
	}
	if !refused {
		t.Errorf("the refusal was not audited: %+v", entries)
	}
	// lowering it takes nothing away, and an operator is not held to it
	if err := r.hub.SetProjectQuota(ctx, op, alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	if refs, _ := r.hub.Projects(ctx, alice); len(refs) != 1 || refs[0].Role != rbac.Owner {
		t.Errorf("alice has %+v", refs)
	}
	if _, _, err := r.hub.CreateLinkCode(ctx, op); err != nil {
		t.Errorf("an operator with no quota: %v", err)
	}
	rows, _ := r.hub.Users(ctx, op)
	for _, row := range rows {
		if row.ID == alice.ID && (row.Projects != 1 || row.ProjectQuota != 0) {
			t.Errorf("the users list says %+v", row)
		}
	}
}

func TestAPersonSeesAndCancelsTheirOwnCodes(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	alice := r.login("alice")
	r.hub.SetProjectQuota(ctx, op, alice.ID, 1)
	alice, _ = r.store.User(ctx, alice.ID)
	mine, _, _ := r.hub.CreateLinkCode(ctx, alice)
	theirs, _, _ := r.hub.CreateLinkCode(ctx, op)
	if list, _ := r.hub.LinkCodes(ctx, alice); len(list) != 1 || list[0].ID != mine.ID {
		t.Errorf("alice lists %+v", list)
	}
	if err := r.hub.CancelLinkCode(ctx, alice, theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancelling the operator's: %v", err)
	}
	if err := r.hub.CancelLinkCode(ctx, op, mine.ID); err != nil {
		t.Errorf("an operator cancelling alice's: %v", err)
	}
}

func TestAnOwnersDestroyIsAnUnlinkAndTheHubNeverTouchesTheProject(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(agent("coder-1", "operator"))
	id := r.id("web")
	alice, bob := r.login("alice"), r.login("bob")
	r.store.SetRole(ctx, id, alice.ID, rbac.Owner)
	r.store.SetRole(ctx, id, bob.ID, rbac.Admin)
	if err := r.hub.Unlink(ctx, bob, id); !errors.Is(err, ErrForbidden) {
		t.Errorf("an administrator unlinking: %v", err)
	}
	if got := audited(r, id, "project.unlink"); len(got) != 1 || got[0].Allowed {
		t.Errorf("the refusal: %+v", got)
	}
	if err := r.hub.Unlink(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Project(ctx, alice, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("the project stays: %v", err)
	}
	if _, err := r.store.Registration(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("its record stays: %v", err)
	}
	verdicts := f.Verdicts()
	if last := verdicts[len(verdicts)-1]; last.State != hublink.StateUnregistered {
		t.Errorf("the link heard %+v", last)
	}
	for _, call := range f.Calls() {
		t.Errorf("the hub touched the project: %s", call)
	}
	got := audited(r, id, "project.unlink")
	if !got[0].Allowed || got[0].Actor != rbac.Actor(alice.ID) {
		t.Errorf("audit = %+v", got)
	}
	// the key is unknown now: it takes a new code, and comes back as a new project with no roles
	if err := r.turnedAway(f.Again(), ""); !errors.Is(err, ErrUnregistered) {
		t.Errorf("the link coming back: %v", err)
	}
}

func TestRedeemingAnInviteSaysWhatItChanged(t *testing.T) {
	r := newRig(t)
	r.link("web")
	op := r.operator()
	id := r.id("web")
	alice := r.login("alice")
	r.store.SetRole(ctx, id, alice.ID, rbac.Admin)
	_, code, err := r.hub.InviteToProject(ctx, op, id, rbac.Viewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.AcceptInvite(ctx, alice, code); err != nil {
		t.Fatal(err)
	}
	if got := audited(r, id, "invite.redeem"); len(got) != 1 || !strings.Contains(got[0].Detail, "nothing: admin already") {
		t.Errorf("an invite that changed nothing = %+v", got)
	}

	bob := r.login("bob")
	_, code, err = r.hub.InviteToHub(ctx, op, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.AcceptInvite(ctx, bob, code); err != nil {
		t.Fatal(err)
	}
	if got := audited(r, "", "invite.redeem"); len(got) == 0 || !strings.Contains(got[0].Detail, "operator, project quota 0 -> 3") {
		t.Errorf("an invite that made an operator = %+v", got)
	}
	if got := audited(r, "", "operator.grant"); len(got) != 1 || got[0].Actor != rbac.Actor(op.ID) || !strings.Contains(got[0].Detail, bob.Username) {
		t.Errorf("operator.grant = %+v", got)
	}
}
