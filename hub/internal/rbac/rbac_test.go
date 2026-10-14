package rbac

import "testing"

func TestWhatEachRoleMayDo(t *testing.T) {
	alice := Subject{ID: "alice"}
	cases := []struct {
		role    Role
		action  Action
		creator string
		want    bool
	}{
		{None, View, "", false},
		{None, Spawn, "", false},
		{User, View, "", true},
		{User, Spawn, "", true},
		{User, Operate, "user:alice", true},
		{User, Operate, "user:bob", false},
		{User, Operate, "operator", false},
		{User, Operate, "", false},
		{User, ManageRoles, "", false},
		{Administrator, Operate, "user:bob", true},
		{Administrator, Operate, "operator", true},
		{Administrator, ManageRoles, "", false},
		{Owner, Operate, "user:bob", true},
		{Owner, ManageRoles, "", true},
		{Owner, "something-new", "", false},
	}
	for _, c := range cases {
		if got := Can(alice, c.role, c.action, c.creator); got != c.want {
			t.Errorf("Can(role=%q, %s, creator=%q) = %v, want %v", c.role, c.action, c.creator, got, c.want)
		}
	}
}

func TestAgentsSpawnedFromTheCLIBelongToTheOperator(t *testing.T) {
	operator := Subject{ID: "root", IsOperator: true}
	other := Subject{ID: "alice"}
	if !Can(operator, User, Operate, Operator) {
		t.Error("the operator cannot operate an agent the CLI spawned")
	}
	if Can(other, User, Operate, Operator) {
		t.Error("a user operated an agent the CLI spawned")
	}
}

func TestAServiceOrAgentActorOwnsNothing(t *testing.T) {
	for _, actor := range []string{"service:ci", "agent:coder", "user:", "user", "garbage"} {
		if Can(Subject{ID: "alice"}, User, Operate, actor) {
			t.Errorf("a user operated an agent created by %q", actor)
		}
	}
}

func TestOwnersCannotRemoveOrLowerThemselves(t *testing.T) {
	cases := []struct {
		actorRole Role
		actor     string
		target    string
		role      Role
		want      bool
	}{
		{Owner, "a", "b", User, true},
		{Owner, "a", "b", None, true},
		{Owner, "a", "b", Owner, true},
		{Owner, "a", "a", None, false},
		{Owner, "a", "a", Administrator, false},
		{Owner, "a", "a", Owner, true},
		{Administrator, "a", "b", User, false},
		{User, "a", "b", User, false},
		{Owner, "a", "b", "boss", false},
	}
	for _, c := range cases {
		if got := CanAssign(c.actorRole, c.actor, c.target, c.role); got != c.want {
			t.Errorf("CanAssign(%q %s -> %s as %q) = %v, want %v", c.actorRole, c.actor, c.target, c.role, got, c.want)
		}
	}
}

func TestActorStrings(t *testing.T) {
	if Actor("u1") != "user:u1" {
		t.Error(Actor("u1"))
	}
	if id, ok := UserOf("user:u1"); !ok || id != "u1" {
		t.Error("user:u1 not read back")
	}
	if _, ok := UserOf("operator"); ok {
		t.Error("operator read as a user")
	}
}

func TestRoleOrder(t *testing.T) {
	if !Owner.AtLeast(Administrator) || !Administrator.AtLeast(User) || User.AtLeast(Administrator) || None.AtLeast(None) {
		t.Error("the order of roles is wrong")
	}
}
