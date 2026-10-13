package store

import (
	"context"
	"errors"
	"path/filepath"
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

func TestTheFirstUserIsTheOperatorAndOnlyOne(t *testing.T) {
	s := open(t)
	first, created, err := s.LoginUser(ctx, "issuer\nsub-1", "root@example.com", "Root")
	if err != nil || !created || !first.IsOperator {
		t.Fatalf("first = %+v created=%v err=%v", first, created, err)
	}
	second, created, _ := s.LoginUser(ctx, "issuer\nsub-2", "b@example.com", "B")
	if !created || second.IsOperator {
		t.Fatalf("second = %+v: a second operator", second)
	}
	again, created, _ := s.LoginUser(ctx, "issuer\nsub-1", "root@example.com", "Root")
	if created || again.ID != first.ID || !again.IsOperator {
		t.Fatalf("a returning login made %+v (created=%v)", again, created)
	}
	operator, err := s.Operator(ctx)
	if err != nil || operator.ID != first.ID {
		t.Errorf("operator = %+v, %v", operator, err)
	}
}

func TestALoginUpdatesTheNameAndEmail(t *testing.T) {
	s := open(t)
	u, _, _ := s.LoginUser(ctx, "i\ns", "old@example.com", "Old")
	s.LoginUser(ctx, "i\ns", "new@example.com", "New")
	got, _ := s.User(ctx, u.ID)
	if got.Email != "new@example.com" || got.Name != "New" {
		t.Errorf("user = %+v", got)
	}
}

func TestRolesAreGrantedReplacedAndRemoved(t *testing.T) {
	s := open(t)
	u, _, _ := s.LoginUser(ctx, "i\n1", "a@x", "A")
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

func TestAnOrphanProjectIsClaimedOnce(t *testing.T) {
	s := open(t)
	a, _, _ := s.LoginUser(ctx, "i\n1", "a@x", "A")
	b, _, _ := s.LoginUser(ctx, "i\n2", "b@x", "B")
	s.SetRole(ctx, "local", "web", a.ID, rbac.User) // a lower role is replaced by the claim
	if claimed, err := s.ClaimIfOrphan(ctx, "local", "web", a.ID); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if role, _ := s.Role(ctx, "local", "web", a.ID); role != rbac.Owner {
		t.Errorf("role = %q", role)
	}
	if claimed, _ := s.ClaimIfOrphan(ctx, "local", "web", b.ID); claimed {
		t.Error("a project that has an owner was claimed")
	}
	if owned, _ := s.HasOwner(ctx, "local", "web"); !owned {
		t.Error("HasOwner = false")
	}
	if owned, _ := s.HasOwner(ctx, "local", "other"); owned {
		t.Error("HasOwner = true for a project nobody owns")
	}
}

func TestSessionsExpireAndStoreOnlyAHash(t *testing.T) {
	s := open(t)
	u, _, _ := s.LoginUser(ctx, "i\n1", "a@x", "A")
	token, _ := s.NewSession(ctx, u.ID, time.Hour)
	if got, err := s.SessionUser(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("session user = %+v, %v", got, err)
	}
	var stored string
	s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == token || stored == "" {
		t.Errorf("the token is stored as %q", stored)
	}
	expired, _ := s.NewSession(ctx, u.ID, -time.Minute)
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

func TestDeletingAUserRemovesTheirGrantsAndSessionsButNeverTheOperator(t *testing.T) {
	s := open(t)
	op, _, _ := s.LoginUser(ctx, "i\n1", "op@x", "Op")
	u, _, _ := s.LoginUser(ctx, "i\n2", "u@x", "U")
	s.SetRole(ctx, "local", "web", u.ID, rbac.User)
	token, _ := s.NewSession(ctx, u.ID, time.Hour)
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if grants, _ := s.GrantsOn(ctx, "local", "web"); len(grants) != 0 {
		t.Errorf("grants left: %+v", grants)
	}
	if _, err := s.SessionUser(ctx, token); err == nil {
		t.Error("the session survived")
	}
	if err := s.DeleteUser(ctx, op.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting the operator: %v", err)
	}
}

func TestEnginesAreStoredAndTheirGrantsGoWithThem(t *testing.T) {
	s := open(t)
	u, _, _ := s.LoginUser(ctx, "i\n1", "a@x", "A")
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
