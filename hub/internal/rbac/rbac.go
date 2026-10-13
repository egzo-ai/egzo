// Package rbac is the hub's permission model: three roles granted per (engine, project), and the rules that
// say what each may do. It knows nothing of users, engines or the web.
package rbac

import "strings"

// Role is what a user holds on one project.
type Role string

const (
	None          Role = ""
	User          Role = "user"
	Administrator Role = "administrator"
	Owner         Role = "owner"
)

// Roles lists the roles that can be granted, weakest first.
var Roles = []Role{User, Administrator, Owner}

// Valid reports whether r can be granted.
func (r Role) Valid() bool { return r == User || r == Administrator || r == Owner }

func (r Role) rank() int {
	switch r {
	case User:
		return 1
	case Administrator:
		return 2
	case Owner:
		return 3
	}
	return 0
}

// AtLeast reports whether r is as strong as other.
func (r Role) AtLeast(other Role) bool { return r.rank() >= other.rank() && r.rank() > 0 }

// Action is something done to a project or one of its agents.
type Action string

const (
	View           Action = "view"            // see the project, its agents, events and logs
	Spawn          Action = "spawn"           // make an agent
	Operate        Action = "operate"         // start, stop, remove, attach (write) an agent
	ManageRoles    Action = "manage-roles"    // grant and remove roles
	DestroyProject Action = "destroy-project" // project down
)

// Operator is the actor string of the CLI and of agents spawned from it.
const Operator = "operator"

// Subject is a signed-in user as the rules see them.
type Subject struct {
	ID         string
	IsOperator bool
}

// Actor is the string an agent's `actor` label holds for a user.
func Actor(userID string) string { return "user:" + userID }

// UserOf returns the user id an `actor` label names, and false for "operator", services and agents.
func UserOf(actor string) (string, bool) {
	id, ok := strings.CutPrefix(actor, "user:")
	return id, ok && id != ""
}

// Can reports whether a subject holding role may do action. creator is the `actor` label of the agent the action is about,
// empty when it is about the project. Agents spawned from the CLI ("operator") belong to the operator.
func Can(subject Subject, role Role, action Action, creator string) bool {
	switch action {
	case View, Spawn:
		return role.AtLeast(User)
	case Operate:
		if role.AtLeast(Administrator) {
			return true
		}
		return role == User && ownedBy(subject, creator)
	case ManageRoles, DestroyProject:
		return role == Owner
	}
	return false
}

func ownedBy(subject Subject, creator string) bool {
	if creator == Operator {
		return subject.IsOperator
	}
	id, ok := UserOf(creator)
	return ok && id == subject.ID
}

// CanAssign reports whether actor, who holds actorRole, may change target's role to newRole (None removes it). An owner cannot
// remove themselves or lower their own role; an owner can change other owners.
func CanAssign(actorRole Role, actorID, targetID string, newRole Role) bool {
	if actorRole != Owner {
		return false
	}
	if newRole != None && !newRole.Valid() {
		return false
	}
	if actorID == targetID && newRole != Owner {
		return false
	}
	return true
}
