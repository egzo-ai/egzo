package hub

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/store"
)

// restart sets the sign-in up again, as a new start of the hub with these settings, and returns what it logged.
func (r *rig) restart(o SignInOptions) (generated, logged string, err error) {
	r.t.Helper()
	var buf bytes.Buffer
	r.hub.log = slog.New(slog.NewTextHandler(&buf, nil))
	generated, err = r.hub.SetupSignIn(ctx, o)
	return generated, buf.String(), err
}

func (r *rig) count(table string) int {
	r.t.Helper()
	users, _ := r.store.Users(ctx)
	identities, _ := r.store.Identities(ctx, "")
	return map[string]int{"users": len(users), "identities": len(identities)}[table]
}

func TestWithNoPasswordSetTheStarterAccountGetsOneOnceAndKeepsIt(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := New(s, nil, nil)
	generated, err := h.SetupSignIn(ctx, SignInOptions{LocalLogin: true})
	if err != nil || len(generated) < 24 {
		t.Fatalf("first start: %q %v", generated, err)
	}
	if _, err := h.PasswordLogin(ctx, "admin", generated); err != nil {
		t.Fatalf("the printed password does not sign in: %v", err)
	}
	again, err := h.SetupSignIn(ctx, SignInOptions{LocalLogin: true})
	if err != nil || again != "" {
		t.Errorf("a second start printed a password again: %q %v", again, err)
	}
	if _, err := h.PasswordLogin(ctx, "ADMIN", generated); err != nil {
		t.Errorf("the password changed on a restart, or the username is case-sensitive: %v", err)
	}
}

func TestTheEnvironmentPasswordIsSavedAtEveryStartAndAChangeEndsPasswordSessions(t *testing.T) {
	r := newRig(t)
	admin := r.operator()
	token, _ := r.store.NewSession(ctx, admin.ID, store.MethodPassword, "", time.Hour)
	if _, _, err := r.restart(SignInOptions{LocalLogin: true, AdminPassword: "admin-password", Issuer: testIssuer}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.SessionUser(ctx, token); err != nil {
		t.Error("the same password ended the sessions")
	}
	generated, logged, err := r.restart(SignInOptions{LocalLogin: true, AdminPassword: "rotated", Issuer: testIssuer})
	if err != nil || generated != "" || !strings.Contains(logged, "admin password changed") {
		t.Fatalf("rotate: %q %q %v", generated, logged, err)
	}
	if _, err := r.store.SessionUser(ctx, token); err == nil {
		t.Error("a password session survived the rotation")
	}
	if _, err := r.hub.PasswordLogin(ctx, "admin", "admin-password"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("the old password still works: %v", err)
	}
	if _, err := r.hub.PasswordLogin(ctx, "admin", "rotated"); err != nil {
		t.Errorf("the new password: %v", err)
	}
}

func TestTheStarterAccountIsRepairedAtStart(t *testing.T) {
	r := newRig(t)
	admin := r.operator()
	alice := r.login("alice")
	r.hub.SetOperator(ctx, admin, alice.ID, true)
	r.hub.SetOperator(ctx, alice, admin.ID, false)
	r.restart(SignInOptions{LocalLogin: true, AdminPassword: "admin-password", Issuer: testIssuer})
	if got, _ := r.store.User(ctx, admin.ID); !got.IsOperator {
		t.Error("admin is not an operator again after a start")
	}
}

func TestEveryFailedPasswordSignInAnswersTheSameAndIsAudited(t *testing.T) {
	r := newRig(t)
	r.login("alice")
	for _, c := range []struct{ user, password string }{{"admin", "wrong"}, {"nobody", "admin-password"}, {"alice", "anything"}} {
		if _, err := r.hub.PasswordLogin(ctx, c.user, c.password); !errors.Is(err, ErrBadLogin) {
			t.Errorf("%s/%s: %v", c.user, c.password, err)
		}
	}
	entries, _ := r.store.AuditOf(ctx, "", "", 10)
	refused := 0
	for _, e := range entries {
		if e.Action == "user.signin" && !e.Allowed {
			refused++
		}
	}
	if refused != 3 {
		t.Errorf("refusals audited: %d", refused)
	}
}

func TestWithLocalLoginOffThereIsNoPasswordAndItsSessionsEnd(t *testing.T) {
	r := newRig(t)
	admin := r.operator()
	token, _ := r.store.NewSession(ctx, admin.ID, store.MethodPassword, "", time.Hour)
	r.store.LinkIdentity(ctx, admin.ID, testIssuer, "admin-sub", store.Profile{})
	_, logged, err := r.restart(SignInOptions{LocalLogin: false, AdminPassword: "ignored", Issuer: testIssuer})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged, "EGZO_ADMIN_PASSWORD is ignored") {
		t.Errorf("no warning about the ignored password: %s", logged)
	}
	if _, err := r.store.SessionUser(ctx, token); err == nil {
		t.Error("a password session survived local login being turned off")
	}
	if _, err := r.hub.PasswordLogin(ctx, "admin", "admin-password"); !errors.Is(err, ErrNoLocalLogin) {
		t.Errorf("a password sign-in with local login off: %v", err)
	}
	if u, _, err := r.hub.IdentityLogin(ctx, testIssuer, "admin-sub", store.Profile{}); err != nil || u.ID != admin.ID {
		t.Errorf("admin through SSO: %+v %v", u, err)
	}
}

func TestAHubThatNobodyCouldManageDoesNotStartAndALockoutIsReported(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := New(s, nil, nil)
	if _, err := h.SetupSignIn(ctx, SignInOptions{LocalLogin: false, Issuer: testIssuer}); !errors.Is(err, ErrNoOperator) || !strings.Contains(err.Error(), "EGZO_LOCAL_LOGIN=true") {
		t.Errorf("a fresh hub with local login off: %v", err)
	}
	r := newRig(t)
	_, logged, err := r.restart(SignInOptions{LocalLogin: false, Issuer: testIssuer})
	if err != nil || !strings.Contains(logged, "no operator has an account at the configured login provider") || !strings.Contains(logged, testIssuer) {
		t.Errorf("no lockout warning: %v %s", err, logged)
	}
}

func TestChangingTheIssuerEndsTheSessionsOfTheOldOne(t *testing.T) {
	r := newRig(t)
	alice := r.login("alice")
	_, identity, _ := r.hub.IdentityLogin(ctx, testIssuer, "alice", store.Profile{})
	token, _ := r.store.NewSession(ctx, alice.ID, store.MethodIdentity, identity, time.Hour)
	_, logged, err := r.restart(SignInOptions{LocalLogin: true, AdminPassword: "admin-password", Issuer: "https://new.example.com"})
	if err != nil || !strings.Contains(logged, testIssuer) || !strings.Contains(logged, "https://new.example.com") {
		t.Fatalf("%v %s", err, logged)
	}
	if _, err := r.store.SessionUser(ctx, token); err == nil {
		t.Error("a session of the old issuer survived")
	}
	if _, _, err := r.hub.IdentityLogin(ctx, testIssuer, "alice", store.Profile{}); err == nil {
		t.Error("an identity of the old issuer signed in")
	}
	if u, _, _ := r.hub.IdentityLogin(ctx, "https://new.example.com", "alice", store.Profile{PreferredUsername: "alice"}); u.ID == alice.ID || u.Username != "alice-2" {
		t.Errorf("the same person at the new provider is %+v", u)
	}
}

func TestADisabledUserCannotSignInEitherWayAndNoNewUserIsMade(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	bob := r.login("bob")
	if _, err := r.hub.SetDisabled(ctx, op, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	users, identities := r.count("users"), r.count("identities")
	if _, _, err := r.hub.IdentityLogin(ctx, testIssuer, "bob", store.Profile{}); !errors.Is(err, ErrSignInRefused) {
		t.Errorf("SSO: %v", err)
	}
	if r.count("users") != users || r.count("identities") != identities {
		t.Error("a disabled user's sign-in made a row")
	}
	if _, err := r.hub.SetDisabled(ctx, op, op.ID, true); !errors.Is(err, ErrCannotDisable) {
		t.Errorf("an operator disabled themselves: %v", err)
	}
	if _, err := r.hub.SetDisabled(ctx, bob, op.ID, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("a user disabled somebody: %v", err)
	}
	// only the starter account has a password today; give bob one to check the password path refuses a disabled user too
	r.store.SetPassword(ctx, bob.ID, "x")
	if _, err := r.hub.PasswordLogin(ctx, "bob", "x"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("a disabled user's password sign-in: %v", err)
	}
}

func TestLinkUnlinkAndRemoveAreAudited(t *testing.T) {
	r := newRig(t)
	op := r.operator()
	accident := r.login("admin-at-idp")
	if _, err := r.hub.LinkIdentity(ctx, op, testIssuer, "admin-at-idp", store.Profile{}); err == nil {
		t.Fatal("an identity moved")
	}
	if err := r.hub.RemoveIdentity(ctx, op, accident.ID, "nope"); !errors.Is(err, ErrNotDisabled) {
		t.Errorf("remove from an enabled user: %v", err)
	}
	r.hub.SetDisabled(ctx, op, accident.ID, true)
	identities, _ := r.store.Identities(ctx, accident.ID)
	if err := r.hub.RemoveIdentity(ctx, op, accident.ID, identities[0].ID); err != nil {
		t.Fatal(err)
	}
	identity, err := r.hub.LinkIdentity(ctx, op, testIssuer, "admin-at-idp", store.Profile{Email: "admin@corp.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.LinkIdentity(ctx, op, "https://elsewhere", "x", store.Profile{}); err == nil {
		t.Error("an identity of another issuer than the configured one was linked")
	}
	if _, err := r.hub.UnlinkIdentity(ctx, op, identity.ID); err != nil {
		t.Errorf("unlink with a password left: %v", err)
	}
	entries, _ := r.store.AuditOf(ctx, "", "", 20)
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.HasPrefix(entries[i].Action, "identity.") {
			got = append(got, entries[i].Action+":"+map[bool]string{true: "allowed", false: "refused"}[entries[i].Allowed])
		}
	}
	want := "identity.link:refused,identity.remove:allowed,identity.link:allowed,identity.unlink:allowed"
	if strings.Join(got, ",") != want {
		t.Errorf("audit = %s, want %s", strings.Join(got, ","), want)
	}
}

func TestADisabledUserIsNoCandidateAndGetsNoRole(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web"))
	r.scan()
	op := r.operator()
	bob := r.login("bob")
	r.hub.SetDisabled(ctx, op, bob.ID, true)
	candidates, _ := r.hub.Candidates(ctx, op, "local", "web")
	for _, c := range candidates {
		if c.ID == bob.ID {
			t.Error("a disabled user is a candidate")
		}
	}
	if err := r.hub.SetRole(ctx, op, "local", "web", bob.ID, "user"); !errors.Is(err, ErrDisabled) {
		t.Errorf("a role for a disabled user: %v", err)
	}
	if err := r.hub.SetOperator(ctx, op, bob.ID, true); !errors.Is(err, ErrDisabled) {
		t.Errorf("a disabled user made an operator: %v", err)
	}
}
