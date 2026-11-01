package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestAnInviteIsGoodOnceAndKeptAsAHash(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	owner := login(t, s, "owner")
	s.SetRole(ctx, web, owner.ID, rbac.Owner)
	made, code, err := s.CreateInvite(ctx, Invite{CreatedBy: owner.ID, Project: web, Role: rbac.User}, time.Now())
	if err != nil || !strings.HasPrefix(code, InvitePrefix) || made.Expires.Sub(made.Created) != InviteLife {
		t.Fatalf("made = %+v %q %v", made, code, err)
	}
	var stored []byte
	s.db.QueryRow(`SELECT hash FROM invites WHERE id = ?`, made.ID).Scan(&stored)
	if strings.Contains(string(stored), code) || len(stored) != 32 {
		t.Errorf("stored = %x", stored)
	}
	if i, err := s.InviteByCode(ctx, code, time.Now()); err != nil || i.Project != web || i.Role != rbac.User || i.CreatedBy != owner.ID {
		t.Errorf("read back = %+v %v", i, err)
	}
	alice, bob := login(t, s, "alice"), login(t, s, "bob")
	if _, err := s.RedeemInvite(ctx, code, alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.Role(ctx, web, alice.ID); role != rbac.User {
		t.Errorf("role = %q", role)
	}
	if _, err := s.RedeemInvite(ctx, code, bob.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("spent twice: %v", err)
	}
	if _, err := s.InviteByCode(ctx, code, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("read once spent: %v", err)
	}
}

func TestAnInviteExpiresAndIsRevoked(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	alice := login(t, s, "alice")
	_, expired, _ := s.CreateInvite(ctx, Invite{CreatedBy: admin.ID}, time.Now().Add(-InviteLife))
	if _, err := s.RedeemInvite(ctx, expired, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("expired: %v", err)
	}
	made, revoked, _ := s.CreateInvite(ctx, Invite{CreatedBy: admin.ID}, time.Now())
	if err := s.RevokeInvite(ctx, made.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, made.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked twice: %v", err)
	}
	if _, err := s.RedeemInvite(ctx, revoked, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("revoked: %v", err)
	}
	if err := s.PurgeInvites(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Invites(ctx, "", time.Now().Add(-2*InviteLife)); len(list) != 0 {
		t.Errorf("left after the purge: %+v", list)
	}
}

func TestAnInviteIsWorthNothingOnceItsMakerMayNoLongerMakeIt(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	alice := login(t, s, "alice")
	first, second := login(t, s, "first"), login(t, s, "second")
	s.SetRole(ctx, web, first.ID, rbac.Owner)
	s.SetRole(ctx, web, second.ID, rbac.Owner)
	_, lowered, _ := s.CreateInvite(ctx, Invite{CreatedBy: first.ID, Project: web, Role: rbac.Admin}, time.Now())
	_, disabled, _ := s.CreateInvite(ctx, Invite{CreatedBy: second.ID, Project: web, Role: rbac.Admin}, time.Now())
	if err := s.SetRole(ctx, web, first.ID, rbac.User); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, lowered, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("of a former owner: %v", err)
	}
	if _, _, err := s.SetDisabled(ctx, second.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, disabled, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("of a disabled owner: %v", err)
	}
	op := operatorNamed(t, s, "op")
	operatorNamed(t, s, "keep")
	_, flags, _ := s.CreateInvite(ctx, Invite{CreatedBy: op.ID, ProjectQuota: 2}, time.Now())
	s.SetOperator(ctx, op.ID, false)
	if _, err := s.RedeemInvite(ctx, flags, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("of a former operator: %v", err)
	}
	if role, _ := s.Role(ctx, web, alice.ID); role != rbac.None {
		t.Errorf("a refused invite gave %q", role)
	}
}

func TestAnInviteIsGoneForGoodOnceItsMakerLosesTheRight(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	alice := login(t, s, "alice")
	first, second := login(t, s, "first"), login(t, s, "second")
	s.SetRole(ctx, web, first.ID, rbac.Owner)
	s.SetRole(ctx, web, second.ID, rbac.Owner)
	op := operatorNamed(t, s, "op")
	operatorNamed(t, s, "keep")
	_, lowered, _ := s.CreateInvite(ctx, Invite{CreatedBy: first.ID, Project: web, Role: rbac.Admin}, time.Now())
	_, disabled, _ := s.CreateInvite(ctx, Invite{CreatedBy: second.ID, Project: web, Role: rbac.Admin}, time.Now())
	_, flags, _ := s.CreateInvite(ctx, Invite{CreatedBy: op.ID, ProjectQuota: 2}, time.Now())
	kept, _, _ := s.CreateInvite(ctx, Invite{CreatedBy: operatorNamed(t, s, "other").ID}, time.Now())

	s.SetRole(ctx, web, first.ID, rbac.User)
	s.SetRole(ctx, web, first.ID, rbac.Owner)
	s.SetDisabled(ctx, second.ID, true)
	s.SetDisabled(ctx, second.ID, false)
	s.SetOperator(ctx, op.ID, false)
	s.SetOperator(ctx, op.ID, true)

	for what, code := range map[string]string{"a former owner's": lowered, "a disabled owner's": disabled, "a former operator's": flags} {
		if _, err := s.RedeemInvite(ctx, code, alice.ID, time.Now()); !errors.Is(err, ErrBadInvite) {
			t.Errorf("%s, once the right came back: %v", what, err)
		}
	}
	if listed, _ := s.Invites(ctx, "", time.Now()); len(listed) != 1 || listed[0].ID != kept.ID {
		t.Errorf("still listed = %+v", listed)
	}
	entries, _ := s.AuditOf(ctx, web, 10)
	if len(entries) != 2 || entries[0].Actor != "hub" || entries[0].Action != "invite.revoke" || !entries[0].Allowed {
		t.Errorf("the audit log = %+v", entries)
	}
}

func TestARevokedProjectLosesItsInvites(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	owner := login(t, s, "owner")
	s.SetRole(ctx, web, owner.ID, rbac.Owner)
	s.CreateInvite(ctx, Invite{CreatedBy: owner.ID, Project: web, Role: rbac.User}, time.Now())
	if _, err := s.Revoke(ctx, web, "no"); err != nil {
		t.Fatal(err)
	}
	if listed, _ := s.Invites(ctx, web, time.Now()); len(listed) != 0 {
		t.Errorf("still listed = %+v", listed)
	}
}

func TestAnInviteOnlyEverAdds(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	owner := login(t, s, "owner")
	s.SetRole(ctx, web, owner.ID, rbac.Owner)
	invite := func(role rbac.Role) string {
		_, code, _ := s.CreateInvite(ctx, Invite{CreatedBy: owner.ID, Project: web, Role: role}, time.Now())
		return code
	}
	if _, err := s.RedeemInvite(ctx, invite(rbac.Viewer), owner.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.Role(ctx, web, owner.ID); role != rbac.Owner {
		t.Errorf("an invite lowered an owner to %q", role)
	}
	alice := login(t, s, "alice")
	s.RedeemInvite(ctx, invite(rbac.User), alice.ID, time.Now())
	s.RedeemInvite(ctx, invite(rbac.Admin), alice.ID, time.Now())
	if role, _ := s.Role(ctx, web, alice.ID); role != rbac.Admin {
		t.Errorf("a higher invite did not raise: %q", role)
	}
	admin, _ := s.EnsureStarter(ctx)
	s.SetProjectQuota(ctx, alice.ID, 5)
	_, flags, _ := s.CreateInvite(ctx, Invite{CreatedBy: admin.ID, ProjectQuota: 2, Operator: true}, time.Now())
	if _, err := s.RedeemInvite(ctx, flags, alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.User(ctx, alice.ID); u.ProjectQuota != 5 || !u.IsOperator {
		t.Errorf("flags = %+v", u)
	}
}

func TestASignInOfAStrangerTakesAnInviteAndAFailedOneWritesNothing(t *testing.T) {
	s := open(t)
	before := count(t, s, "users")
	if _, _, _, _, err := s.LoginIdentity(ctx, issuer, "stranger", Profile{}, ""); !errors.Is(err, ErrNoInvite) {
		t.Errorf("without an invite: %v", err)
	}
	if _, _, _, _, err := s.LoginIdentity(ctx, issuer, "stranger", Profile{}, InvitePrefix+"nonsense"); !errors.Is(err, ErrBadInvite) {
		t.Errorf("with a bad invite: %v", err)
	}
	if n := count(t, s, "users"); n != before {
		t.Errorf("users went from %d to %d", before, n)
	}
	if n := count(t, s, "user_identities"); n != 0 {
		t.Errorf("identities = %d", n)
	}
	code := invited(t, s)
	u, _, spent, created, err := s.LoginIdentity(ctx, issuer, "stranger", Profile{PreferredUsername: "sam"}, code)
	if err != nil || !created || u.Username != "sam" || spent.ID == "" {
		t.Fatalf("with an invite: %+v %+v %v %v", u, spent, created, err)
	}
	// a person the hub knows signs in with no invite, and one they brought is left alone
	code = invited(t, s)
	if _, _, spent, created, err := s.LoginIdentity(ctx, issuer, "stranger", Profile{}, code); err != nil || created || spent.ID != "" {
		t.Errorf("signing in again: %+v %v %v", spent, created, err)
	}
	if _, err := s.InviteByCode(ctx, code, time.Now()); err != nil {
		t.Errorf("the invite of a known person went: %v", err)
	}
}

func TestALocalAccountIsMadeFromAnInviteOrNotAtAll(t *testing.T) {
	s := open(t)
	if _, _, err := s.RegisterLocal(ctx, InvitePrefix+"nonsense", "sam", "hash", time.Now()); !errors.Is(err, ErrBadInvite) {
		t.Errorf("a bad code: %v", err)
	}
	if _, err := s.UserByName(ctx, "sam"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad code made a user: %v", err)
	}
	code := invited(t, s)
	if _, _, err := s.RegisterLocal(ctx, code, "ADMIN", "hash", time.Now()); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("a name somebody has: %v", err)
	}
	u, _, err := s.RegisterLocal(ctx, code, "sam", "hash", time.Now())
	if err != nil || !u.HasPassword {
		t.Fatalf("registered = %+v %v", u, err)
	}
	if hash, _ := s.PasswordHash(ctx, u.ID); hash != "hash" {
		t.Errorf("hash = %q", hash)
	}
}

func TestInvitesGoWithTheirProject(t *testing.T) {
	s := open(t)
	web := registered(t, s, "web")
	owner := login(t, s, "owner")
	s.SetRole(ctx, web, owner.ID, rbac.Owner)
	s.CreateInvite(ctx, Invite{CreatedBy: owner.ID, Project: web, Role: rbac.User}, time.Now())
	admin, _ := s.EnsureStarter(ctx)
	s.CreateInvite(ctx, Invite{CreatedBy: admin.ID}, time.Now())
	if list, _ := s.Invites(ctx, web, time.Now()); len(list) != 1 {
		t.Errorf("of the project: %+v", list)
	}
	if err := s.ForgetProject(ctx, web, "approved"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Invites(ctx, "", time.Now()); len(list) != 1 || list[0].Project != "" {
		t.Errorf("left: %+v", list)
	}
}

func TestAPersonChoosesOnlyANameTheHubWouldWrite(t *testing.T) {
	for name, want := range map[string]bool{"sam": true, "sam.lee-2": true, "": false, "Sam": false, "sam lee": false, "admin": false, "operator": false, "hub": false, "a@b": false, "-sam": false} {
		if got := ValidUsername(name); got != want {
			t.Errorf("ValidUsername(%q) = %v", name, got)
		}
	}
}
