package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var ctx = context.Background()

const issuer = "https://idp.example.com"

// login signs in at the test issuer as sub, with a profile named after it.
func login(t *testing.T, s *Store, sub string) User {
	t.Helper()
	u, _, _, err := s.LoginIdentity(ctx, issuer, sub, Profile{Email: sub + "@example.com", Name: sub, PreferredUsername: sub})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestADatabaseOfAnotherVersionIsRefusedAndANewOneIsMade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`PRAGMA user_version = 0`)
	s.Close()
	if _, err := Open(path); !errors.Is(err, ErrOldSchema) || !strings.Contains(err.Error(), "remove "+path) {
		t.Errorf("an old database: %v", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "new.db")); err != nil {
		t.Errorf("a new database: %v", err)
	}
}

func TestANewIdentityMakesAUserWithNoAccessAndAReturningOneFindsThem(t *testing.T) {
	s := open(t)
	u, identity, created, err := s.LoginIdentity(ctx, issuer, "s1", Profile{Email: "alex@corp.com", Name: "Alex", PreferredUsername: "alex@corp.com"})
	if err != nil || !created || u.IsOperator || u.Username != "alex" || identity == "" {
		t.Fatalf("first sign-in: %+v %q created=%v %v", u, identity, created, err)
	}
	again, identity2, created, _ := s.LoginIdentity(ctx, issuer, "s1", Profile{Email: "alex@new.com", Name: "Alex B", PreferredUsername: "alexb"})
	if created || again.ID != u.ID || identity2 != identity {
		t.Fatalf("a returning sign-in made %+v (created=%v)", again, created)
	}
	if again.Username != "alex" || again.Email != "alex@new.com" || again.Name != "Alex B" {
		t.Errorf("the username follows the provider, or the snapshot was not refreshed: %+v", again)
	}
	if other := login(t, s, "s2"); other.ID == u.ID {
		t.Error("another subject found the same user")
	}
	if u3, _, created, _ := s.LoginIdentity(ctx, "https://other.example.com", "s1", Profile{}); !created || u3.ID == u.ID {
		t.Error("the same subject at another issuer found the same user")
	}
}

func TestNothingIsMatchedByEmail(t *testing.T) {
	s := open(t)
	a, _, _, _ := s.LoginIdentity(ctx, issuer, "a", Profile{Email: "same@corp.com", EmailVerified: true})
	b, _, created, _ := s.LoginIdentity(ctx, issuer, "b", Profile{Email: "same@corp.com", EmailVerified: true})
	if !created || a.ID == b.ID || b.Username != "same-2" {
		t.Errorf("two subjects with one email: %+v %+v", a, b)
	}
}

func TestADisabledUserIsRefusedAndNoNewUserIsMadeNorAnythingRefreshed(t *testing.T) {
	s := open(t)
	u := login(t, s, "bob")
	if _, _, err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	users, identities := count(t, s, "users"), count(t, s, "user_identities")
	_, _, _, err := s.LoginIdentity(ctx, issuer, "bob", Profile{Email: "changed@example.com"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("a disabled user signed in: %v", err)
	}
	if count(t, s, "users") != users || count(t, s, "user_identities") != identities {
		t.Error("a disabled user's sign-in made a row")
	}
	if got, _ := s.User(ctx, u.ID); got.Email != "bob@example.com" {
		t.Errorf("a refused sign-in refreshed the snapshot: %+v", got)
	}
}

func TestTheStarterAccountIsMadeAndRepaired(t *testing.T) {
	s := open(t)
	admin, err := s.EnsureStarter(ctx)
	if err != nil || admin.Username != "admin" || !admin.IsOperator || admin.Disabled() {
		t.Fatalf("starter = %+v, %v", admin, err)
	}
	other := login(t, s, "root")
	s.SetOperator(ctx, other.ID, true)
	s.SetOperator(ctx, admin.ID, false)
	again, _ := s.EnsureStarter(ctx)
	if again.ID != admin.ID || !again.IsOperator {
		t.Errorf("the starter account was not repaired: %+v", again)
	}
	if count(t, s, "users") != 2 {
		t.Error("the starter account was made twice")
	}
	if u := login(t, s, "admin"); u.Username != "admin-2" {
		t.Errorf("a provider's admin got %q", u.Username)
	}
}

func TestOperatorsAreMadeAndUnmadeAndTheLastOneCannotBe(t *testing.T) {
	s := open(t)
	a, _ := s.EnsureStarter(ctx)
	b := login(t, s, "b")
	if _, err := s.SetOperator(ctx, a.ID, false); !errors.Is(err, ErrLastOperator) {
		t.Fatalf("demoting the last operator: %v", err)
	}
	if changed, err := s.SetOperator(ctx, b.ID, true); err != nil || !changed {
		t.Fatalf("making b an operator: changed=%v %v", changed, err)
	}
	if changed, err := s.SetOperator(ctx, b.ID, true); err != nil || changed {
		t.Errorf("making b an operator twice: changed=%v %v", changed, err)
	}
	if changed, err := s.SetOperator(ctx, a.ID, false); err != nil || !changed {
		t.Fatalf("demoting a while b is one: changed=%v %v", changed, err)
	}
	if _, err := s.SetOperator(ctx, b.ID, false); !errors.Is(err, ErrLastOperator) {
		t.Errorf("demoting the last operator, now b: %v", err)
	}
	if _, err := s.SetOperator(ctx, "uGHOST", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user that does not exist: %v", err)
	}
	c := login(t, s, "c")
	s.SetDisabled(ctx, c.ID, true)
	if _, err := s.SetOperator(ctx, c.ID, true); !errors.Is(err, ErrDisabled) {
		t.Errorf("a disabled user was made an operator: %v", err)
	}
}

func TestDisablingEndsTheSessionsAndNeverAppliesToAnOperator(t *testing.T) {
	s := open(t)
	op, _ := s.EnsureStarter(ctx)
	u := login(t, s, "u")
	s.SetRole(ctx, "local", "web", u.ID, rbac.User)
	token, _ := s.NewSession(ctx, u.ID, MethodIdentity, "", time.Hour)
	changed, ended, err := s.SetDisabled(ctx, u.ID, true)
	if err != nil || !changed || len(ended) != 1 || ended[0] != HashToken(token) {
		t.Fatalf("disable: changed=%v ended=%v %v", changed, ended, err)
	}
	if _, err := s.SessionUser(ctx, token); err == nil {
		t.Error("the session survived")
	}
	if got, _ := s.User(ctx, u.ID); !got.Disabled() {
		t.Error("not disabled")
	}
	if grants, _ := s.GrantsOf(ctx, u.ID); len(grants) != 1 {
		t.Error("the grants did not stay, dormant")
	}
	if changed, _, _ := s.SetDisabled(ctx, u.ID, true); changed {
		t.Error("disabling twice changed something")
	}
	if changed, _, err := s.SetDisabled(ctx, u.ID, false); err != nil || !changed {
		t.Errorf("enable: %v %v", changed, err)
	}
	if _, _, err := s.SetDisabled(ctx, op.ID, true); !errors.Is(err, ErrOperator) {
		t.Errorf("disabling an operator: %v", err)
	}
}

func TestUsersComeBackInTheOrderTheyWereMade(t *testing.T) {
	s := open(t)
	var want []string
	for i := 0; i < 12; i++ { // within the same second, whatever their ids sort like
		want = append(want, login(t, s, string(rune('a'+i))).ID)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != len(want) {
		t.Fatalf("users = %d, %v", len(users), err)
	}
	for i, u := range users {
		if u.ID != want[i] {
			t.Fatalf("user %d is %s, want %s", i, u.ID, want[i])
		}
	}
}

func TestLinkingAddsAnIdentityAndNeverMovesOne(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	alice := login(t, s, "alice")
	if _, err := s.LinkIdentity(ctx, admin.ID, issuer, "alice", Profile{}); err == nil {
		t.Fatal("alice's identity moved to admin")
	} else if taken := (*IdentityTakenError)(nil); !errors.As(err, &taken) || taken.Username != "alice" {
		t.Fatalf("the refusal does not name the owner: %v", err)
	}
	s.SetDisabled(ctx, alice.ID, true)
	if _, err := s.LinkIdentity(ctx, admin.ID, issuer, "alice", Profile{}); err == nil {
		t.Fatal("a disabled user's identity moved by a link")
	}
	identity, err := s.LinkIdentity(ctx, admin.ID, issuer, "admin-sub", Profile{Email: "admin@corp.com"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.LinkIdentity(ctx, admin.ID, issuer, "admin-sub", Profile{}); err != nil || again.ID != identity.ID {
		t.Errorf("linking the same identity twice: %+v %v", again, err)
	}
	if _, err := s.LinkIdentity(ctx, admin.ID, issuer, "admin-second", Profile{}); !errors.Is(err, ErrIssuerLinked) {
		t.Errorf("a second identity at one issuer: %v", err)
	}
	if _, err := s.LinkIdentity(ctx, admin.ID, "https://next.example.com", "admin-next", Profile{}); err != nil {
		t.Errorf("an identity at another issuer: %v", err)
	}
	u, _, created, _ := s.LoginIdentity(ctx, issuer, "admin-sub", Profile{})
	if created || u.ID != admin.ID {
		t.Errorf("the linked identity signs in as %+v", u)
	}
}

func TestUnlinkingEndsItsSessionsAndKeepsAWayIn(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	s.SetPassword(ctx, admin.ID, "hash")
	identity, _ := s.LinkIdentity(ctx, admin.ID, issuer, "admin-sub", Profile{})
	old, _ := s.LinkIdentity(ctx, admin.ID, "https://old.example.com", "admin-old", Profile{})
	token, _ := s.NewSession(ctx, admin.ID, MethodIdentity, identity.ID, time.Hour)
	password, _ := s.NewSession(ctx, admin.ID, MethodPassword, "", time.Hour)

	// with local login off, the password is no way in: the identity at the configured issuer is the last one
	if _, _, err := s.UnlinkIdentity(ctx, admin.ID, identity.ID, issuer, false); !errors.Is(err, ErrLastWayIn) {
		t.Fatalf("unlinking the last way in: %v", err)
	}
	if _, _, err := s.UnlinkIdentity(ctx, admin.ID, old.ID, issuer, false); err != nil {
		t.Errorf("an identity of a former issuer is never the last way in: %v", err)
	}
	_, ended, err := s.UnlinkIdentity(ctx, admin.ID, identity.ID, issuer, true)
	if err != nil || len(ended) != 1 || ended[0] != HashToken(token) {
		t.Fatalf("unlink with a password left: ended=%v %v", ended, err)
	}
	if _, err := s.SessionUser(ctx, token); err == nil {
		t.Error("the identity's session survived")
	}
	if _, err := s.SessionUser(ctx, password); err != nil {
		t.Error("the password session ended")
	}
	bob := login(t, s, "bob")
	if _, _, err := s.UnlinkIdentity(ctx, admin.ID, bobIdentity(t, s, bob).ID, issuer, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlinking somebody else's identity: %v", err)
	}
}

func bobIdentity(t *testing.T, s *Store, u User) Identity {
	t.Helper()
	list, err := s.Identities(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("identities = %v %v", list, err)
	}
	return list[0]
}

func TestAnOperatorRemovesAnIdentityOnlyFromADisabledUser(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	accident := login(t, s, "admin-at-idp")
	identity := bobIdentity(t, s, accident)
	if _, err := s.RemoveIdentity(ctx, accident.ID, identity.ID); !errors.Is(err, ErrNotDisabled) {
		t.Fatalf("removing an enabled user's identity: %v", err)
	}
	s.SetDisabled(ctx, accident.ID, true)
	if _, err := s.RemoveIdentity(ctx, accident.ID, identity.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIdentity(ctx, admin.ID, issuer, "admin-at-idp", Profile{}); err != nil {
		t.Errorf("the freed identity cannot be linked: %v", err)
	}
}

func TestPasswordsAreTheirOwnRowAndChangingOneEndsThePasswordSessions(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	if _, err := s.PasswordHash(ctx, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a password before any: %v", err)
	}
	s.SetPassword(ctx, admin.ID, "hash-1")
	identity, _ := s.LinkIdentity(ctx, admin.ID, issuer, "admin-sub", Profile{})
	password, _ := s.NewSession(ctx, admin.ID, MethodPassword, "", time.Hour)
	sso, _ := s.NewSession(ctx, admin.ID, MethodIdentity, identity.ID, time.Hour)
	ended, err := s.SetPassword(ctx, admin.ID, "hash-2")
	if err != nil || len(ended) != 1 || ended[0] != HashToken(password) {
		t.Fatalf("set password: ended=%v %v", ended, err)
	}
	if hash, _ := s.PasswordHash(ctx, admin.ID); hash != "hash-2" {
		t.Errorf("hash = %q", hash)
	}
	if _, err := s.SessionUser(ctx, sso); err != nil {
		t.Error("an SSO session ended with the password")
	}
	if got, _ := s.User(ctx, admin.ID); !got.HasPassword {
		t.Error("HasPassword is not set")
	}
}

func TestSessionsOfAFormerIssuerAndPasswordSessionsEndInBulk(t *testing.T) {
	s := open(t)
	admin, _ := s.EnsureStarter(ctx)
	old, _ := s.LinkIdentity(ctx, admin.ID, "https://old.example.com", "a", Profile{})
	current, _ := s.LinkIdentity(ctx, admin.ID, issuer, "a", Profile{})
	oldSession, _ := s.NewSession(ctx, admin.ID, MethodIdentity, old.ID, time.Hour)
	currentSession, _ := s.NewSession(ctx, admin.ID, MethodIdentity, current.ID, time.Hour)
	password, _ := s.NewSession(ctx, admin.ID, MethodPassword, "", time.Hour)
	issuers, ended, err := s.EndSessionsNotAt(ctx, issuer)
	if err != nil || len(issuers) != 1 || issuers[0] != "https://old.example.com" {
		t.Fatalf("issuers = %v %v", issuers, err)
	}
	if len(ended) != 1 || ended[0] != HashToken(oldSession) {
		t.Errorf("sessions of the former issuer ended: %v", ended)
	}
	if _, err := s.SessionUser(ctx, oldSession); err == nil {
		t.Error("a session of the former issuer survived")
	}
	if _, err := s.SessionUser(ctx, currentSession); err != nil {
		t.Error("a session of the current issuer ended")
	}
	if ended, _ := s.EndPasswordSessions(ctx); len(ended) != 1 || ended[0] != HashToken(password) {
		t.Errorf("password sessions ended: %v", ended)
	}
	if _, err := s.SessionUser(ctx, currentSession); err != nil {
		t.Error("ending password sessions ended an SSO one")
	}
}

func TestRolesAreGrantedReplacedAndRemoved(t *testing.T) {
	s := open(t)
	u := login(t, s, "a")
	if role, _ := s.Role(ctx, "local", "web", u.ID); role != rbac.None {
		t.Fatalf("role before any grant = %q", role)
	}
	s.SetRole(ctx, "local", "web", u.ID, rbac.User)
	s.SetRole(ctx, "local", "web", u.ID, rbac.Administrator)
	if role, _ := s.Role(ctx, "local", "web", u.ID); role != rbac.Administrator {
		t.Errorf("role = %q", role)
	}
	if role, _ := s.Role(ctx, "other", "web", u.ID); role != rbac.None {
		t.Errorf("a grant on one engine leaked to another: %q", role)
	}
	if err := s.SetRole(ctx, "local", "web", u.ID, "boss"); err == nil {
		t.Error("an invalid role was stored")
	}
	s.SetRole(ctx, "local", "web", u.ID, rbac.None)
	if grants, _ := s.GrantsOf(ctx, u.ID); len(grants) != 0 {
		t.Errorf("grants after removal: %+v", grants)
	}
}

func TestSessionsExpireAndStoreOnlyAHash(t *testing.T) {
	s := open(t)
	u := login(t, s, "a")
	token, _ := s.NewSession(ctx, u.ID, MethodPassword, "", time.Hour)
	if got, err := s.SessionUser(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("session user = %+v, %v", got, err)
	}
	var stored string
	s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == token || stored == "" {
		t.Errorf("the token is stored as %q", stored)
	}
	expired, _ := s.NewSession(ctx, u.ID, MethodPassword, "", -time.Minute)
	if _, err := s.SessionUser(ctx, expired); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired session works: %v", err)
	}
	s.EndSession(ctx, token)
	if _, err := s.SessionUser(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("a ended session works: %v", err)
	}
	if _, err := s.SessionUser(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty token works: %v", err)
	}
}

func TestEnginesAreStoredAndTheirGrantsGoWithThem(t *testing.T) {
	s := open(t)
	u := login(t, s, "a")
	s.PutEngine(ctx, EngineRow{Name: "lab", Host: "ssh://me@lab", Credentials: []byte("sealed")})
	s.PutEngine(ctx, EngineRow{Name: "lab", Host: "ssh://me@lab2", HostKey: "SHA256:abc", Credentials: []byte("sealed2")})
	e, err := s.Engine(ctx, "lab")
	if err != nil || e.Host != "ssh://me@lab2" || e.HostKey != "SHA256:abc" || string(e.Credentials) != "sealed2" {
		t.Fatalf("engine = %+v, %v", e, err)
	}
	s.SetRole(ctx, "lab", "web", u.ID, rbac.Owner)
	s.SetRole(ctx, "local", "web", u.ID, rbac.Owner)
	if err := s.DeleteEngine(ctx, "lab"); err != nil {
		t.Fatal(err)
	}
	if grants, _ := s.GrantsOf(ctx, u.ID); len(grants) != 1 || grants[0].Engine != "local" {
		t.Errorf("grants = %+v", grants)
	}
	if err := s.DeleteEngine(ctx, "lab"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestTheAuditLogIsKeptPerProjectNewestFirst(t *testing.T) {
	s := open(t)
	s.Audit(ctx, AuditEntry{Actor: "user:u1", Action: "agent.stop", Engine: "local", Project: "web", Agent: "coder-1", Allowed: true})
	s.Audit(ctx, AuditEntry{Actor: "user:u2", Action: "agent.rm", Engine: "local", Project: "web", Agent: "coder-1", Allowed: false, Detail: "not allowed"})
	s.Audit(ctx, AuditEntry{Actor: "user:u2", Action: "agent.rm", Engine: "local", Project: "other"})
	entries, err := s.AuditOf(ctx, "local", "web", 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	if entries[0].Action != "agent.rm" || entries[0].Allowed || entries[1].Action != "agent.stop" || !entries[1].Allowed {
		t.Errorf("entries = %+v", entries)
	}
}

func TestWrongCredentialsAreGroupedByTheNameClaimedAndKeepTheLatestClient(t *testing.T) {
	s := open(t)
	at := time.Now().Truncate(time.Millisecond)
	attempt := func(claimed, client string, n int) Denial {
		return Denial{Engine: "local", Project: "web", ClaimedAgent: claimed, Client: client, UserAgent: "curl/8.11.1", Protocol: "https",
			Host: "example.com", Code: "credentials", Event: "connect", Decision: "deny", Count: 1, First: at.Add(time.Duration(n) * time.Second),
			Last: at.Add(time.Duration(n) * time.Second)}
	}
	if err := s.RecordDenials(ctx, []Denial{attempt("coder-1", "10.0.0.2", 0), attempt("coder-2", "10.0.0.3", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDenials(ctx, []Denial{attempt("coder-1", "10.0.0.4", 2)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Denials(ctx, "local", "web")
	if len(got) != 2 || got[0].ClaimedAgent != "coder-1" || got[0].Count != 2 || got[0].Client != "10.0.0.4" || got[0].UserAgent != "curl/8.11.1" ||
		got[1].ClaimedAgent != "coder-2" || got[1].Agent != "" {
		t.Fatalf("groups = %+v", got)
	}
}

func TestDenialsGroupAndAreBoundedAndAFixHoldsUntilTheNextOne(t *testing.T) {
	s := open(t)
	at := time.Now().Truncate(time.Millisecond)
	group := Denial{Engine: "local", Project: "web", Instance: "01JBX7Q0000000000000000000", Protocol: "https", Host: "evil.example",
		Code: "not-in-profile", Event: "connect", Decision: "deny", Agent: "coder-1", Actor: "user:alice", Profile: "default", Count: 2, First: at, Last: at}
	if err := s.RecordDenials(ctx, []Denial{group}); err != nil {
		t.Fatal(err)
	}
	later := group
	later.Count, later.First, later.Last, later.Actor, later.Text = 1, at.Add(time.Second), at.Add(time.Second), "", "again"
	if err := s.RecordDenials(ctx, []Denial{later}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Denials(ctx, "local", "web")
	if len(got) != 1 || got[0].Count != 3 || !got[0].First.Equal(at) || !got[0].Last.Equal(at.Add(time.Second)) || got[0].Actor != "user:alice" || got[0].Text != "again" {
		t.Fatalf("group = %+v", got)
	}

	// a fix before the last denial does not count; one after it does, until the next denial
	s.FixDenials(ctx, "local", "web", group.Instance, "https", "evil.example", []string{"connect"}, at.Add(500*time.Millisecond))
	if got, _ := s.Denials(ctx, "local", "web"); !got[0].Fixed.IsZero() {
		t.Error("fixed by an allow older than the last denial")
	}
	s.FixDenials(ctx, "local", "web", group.Instance, "https", "evil.example", []string{"connect"}, at.Add(2*time.Second))
	if got, _ := s.Denials(ctx, "local", "web"); !got[0].Fixed.Equal(at.Add(2 * time.Second)) {
		t.Errorf("not fixed: %+v", got[0])
	}
	s.RecordDenials(ctx, []Denial{later})
	if got, _ := s.Denials(ctx, "local", "web"); !got[0].Fixed.IsZero() {
		t.Error("a denial after the fix leaves it fixed")
	}

	// a project keeps MaxDenials groups, the most recent
	var many []Denial
	for i := range MaxDenials + 10 {
		d := group
		d.Host = fmt.Sprintf("h%03d.example", i)
		d.Last = at.Add(time.Duration(i) * time.Minute)
		many = append(many, d)
	}
	s.RecordDenials(ctx, many)
	got, _ = s.Denials(ctx, "local", "web")
	if len(got) != MaxDenials || got[0].Host != fmt.Sprintf("h%03d.example", MaxDenials+9) {
		t.Errorf("%d groups, newest %s", len(got), got[0].Host)
	}
	if err := s.PurgeDenials(ctx, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Denials(ctx, "local", "web"); len(got) != MaxDenials-60+10 {
		t.Errorf("after the purge: %d", len(got))
	}
}

func TestTheAuditCursorOnlyMovesForwardAndGoesWithItsEngine(t *testing.T) {
	s := open(t)
	if at, err := s.AuditCursor(ctx, "local", "web"); err != nil || !at.IsZero() {
		t.Fatalf("a project never read: %v %v", at, err)
	}
	at := time.Date(2026, 10, 23, 12, 0, 0, 123456789, time.UTC)
	s.SetAuditCursor(ctx, "local", "web", at)
	s.SetAuditCursor(ctx, "local", "web", at.Add(-time.Hour))
	if got, _ := s.AuditCursor(ctx, "local", "web"); !got.Equal(at) {
		t.Errorf("cursor = %v", got)
	}
	s.PutEngine(ctx, EngineRow{Name: "local", Host: "unix:///x"})
	s.RecordDenials(ctx, []Denial{{Engine: "local", Project: "web", Host: "h", Count: 1, First: at, Last: at}})
	if err := s.DeleteEngine(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AuditCursor(ctx, "local", "web"); !got.IsZero() {
		t.Error("the cursor outlived its engine")
	}
	if got, _ := s.Denials(ctx, "local", "web"); len(got) != 0 {
		t.Error("the denials outlived their engine")
	}
}

func TestExpiredCodesArePurgedAndAreNotListed(t *testing.T) {
	s := open(t)
	u := operatorNamed(t, s, "a")
	s.CreateLinkCode(ctx, u.ID, time.Now().Add(-time.Hour))
	live, _, _ := s.CreateLinkCode(ctx, u.ID, time.Now())
	if codes, _ := s.LinkCodes(ctx, time.Now()); len(codes) != 1 || codes[0].ID != live.ID {
		t.Errorf("codes = %+v", codes)
	}
	if err := s.PurgeLinkCodes(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var kept int
	s.db.QueryRow(`SELECT COUNT(*) FROM link_codes`).Scan(&kept)
	if kept != 1 {
		t.Errorf("%d codes are kept", kept)
	}
}

// operatorNamed signs someone in and makes them an operator.
func operatorNamed(t *testing.T, s *Store, sub string) User {
	t.Helper()
	u := login(t, s, sub)
	if _, err := s.SetOperator(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	return u
}
