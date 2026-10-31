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
		{Viewer, View, "", false},
		{Viewer, Spawn, "", false},
		{Viewer, Operate, "user:alice", false},
		{Overseer, View, "", true},
		{Overseer, Inspect, "", false},
		{Overseer, Spawn, "", false},
		{Overseer, Operate, "user:alice", false},
		{Overseer, ManageRoles, "", false},
		{Overseer, Unlink, "", false},
		{User, Unlink, "", false},
		{Admin, Unlink, "", false},
		{Owner, Unlink, "", true},
		{User, View, "", true},
		{User, Spawn, "", true},
		{User, Operate, "user:alice", true},
		{User, Operate, "user:bob", false},
		{User, Operate, "operator", false},
		{User, Operate, "", false},
		{User, ManageRoles, "", false},
		{Admin, Operate, "user:bob", true},
		{Admin, Operate, "operator", true},
		{Admin, ManageRoles, "", false},
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

func TestAgentsSpawnedFromTheCLIAreForAdministrators(t *testing.T) {
	operator := Subject{ID: "root", IsOperator: true}
	if !Can(Subject{ID: "alice"}, Admin, Operate, CLI) {
		t.Error("an administrator cannot operate an agent the CLI spawned")
	}
	for _, s := range []Subject{operator, {ID: "alice"}} {
		if Can(s, User, Operate, CLI) {
			t.Errorf("%s operated an agent the CLI spawned with the role user", s.ID)
		}
	}
}

func TestOperatorsSeeMembersWithoutARole(t *testing.T) {
	if !Can(Subject{ID: "root", IsOperator: true}, Overseer, SeeMembers, "") {
		t.Error("an operator cannot see who holds a project")
	}
	if Can(Subject{ID: "alice"}, Admin, SeeMembers, "") {
		t.Error("an administrator saw the members")
	}
}

func TestAServiceOrAgentActorOwnsNothing(t *testing.T) {
	for _, actor := range []string{"service:ci", "agent:coder", "user:", "user", "garbage"} {
		if Can(Subject{ID: "alice"}, User, Operate, actor) {
			t.Errorf("a user operated an agent created by %q", actor)
		}
	}
}

func TestWhoMayChangeWhichRole(t *testing.T) {
	alice, root := Subject{ID: "a"}, Subject{ID: "op", IsOperator: true}
	cases := []struct {
		name      string
		actor     Subject
		held      Role
		target    string
		role      Role
		ownerless bool
		want      bool
	}{
		{"an owner sets another's role", alice, Owner, "b", User, false, true},
		{"an owner removes another", alice, Owner, "b", None, false, true},
		{"an owner makes another owner", alice, Owner, "b", Owner, false, true},
		{"an owner cannot remove themselves", alice, Owner, "a", None, false, false},
		{"an owner cannot lower themselves", alice, Owner, "a", Admin, false, false},
		{"an owner stays an owner", alice, Owner, "a", Owner, false, true},
		{"an administrator grants nothing", alice, Admin, "b", User, false, false},
		{"a user grants nothing", alice, User, "b", User, false, false},
		{"what is not a role", alice, Owner, "b", "boss", false, false},
		{"overseer is never granted", alice, Owner, "b", Overseer, false, false},
		{"an operator gives themselves a role", root, Overseer, "op", Admin, false, true},
		{"an operator gives themselves owner", root, Overseer, "op", Owner, false, true},
		{"an operator takes their role back", root, Admin, "op", None, false, true},
		{"an operator who owns steps down", root, Owner, "op", None, false, true},
		{"an operator gives others nothing", root, Overseer, "b", Owner, false, false},
		{"an operator removes no owner", root, Overseer, "b", None, false, false},
		{"an operator with a role gives others nothing", root, Admin, "b", User, false, false},
		{"an operator gives an ownerless project an owner", root, Overseer, "b", Owner, true, true},
		{"an ownerless project gets an owner and nothing else", root, Overseer, "b", Admin, true, false},
		{"nobody else fills an ownerless project", alice, Admin, "b", Owner, true, false},
	}
	for _, c := range cases {
		if got := CanAssign(c.actor, c.held, c.target, c.role, c.ownerless); got != c.want {
			t.Errorf("%s: CanAssign = %v, want %v", c.name, got, c.want)
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
	if !Owner.AtLeast(Admin) || !Admin.AtLeast(User) || !User.AtLeast(Viewer) || Viewer.AtLeast(User) || User.AtLeast(Admin) || None.AtLeast(None) {
		t.Error("the order of roles is wrong")
	}
	if Overseer.AtLeast(Viewer) || Overseer.Valid() {
		t.Error("overseer is a role")
	}
}
