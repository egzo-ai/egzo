// Package rbac is the hub's permission model: four roles granted per project, and the rules that say what each may do. It knows
// nothing of users, projects or the web.
package rbac

import "strings"

// Role is what a user holds on one project.
type Role string

const (
	None   Role = ""
	Viewer Role = "viewer" // the web proxy only: nothing of the project is shown to them
	User   Role = "user"
	Admin  Role = "admin"
	Owner  Role = "owner"
	// Overseer is what an operator has on a project they hold no role on. Nobody grants it: it shows the project and its agents, and
	// going further takes a role, which the operator grants themselves.
	Overseer Role = "overseer"
)

// Roles lists the roles that can be granted, weakest first.
var Roles = []Role{Viewer, User, Admin, Owner}

// Valid reports whether r can be granted.
func (r Role) Valid() bool { return r.rank() > 0 }

func (r Role) rank() int {
	switch r {
	case Viewer:
		return 1
	case User:
		return 2
	case Admin:
		return 3
	case Owner:
		return 4
	}
	return 0
}

// AtLeast reports whether r is as strong as other. Overseer is not on the scale: it is at least nothing.
func (r Role) AtLeast(other Role) bool { return r.rank() >= other.rank() && r.rank() > 0 }

// Action is something done to a project or one of its agents.
type Action string

const (
	View        Action = "view"         // see the project and its agents, with the task on each and how it went
	Inspect     Action = "inspect"      // what a role gives on top of that: the timeline, the project's activity, the resolved project
	Spawn       Action = "spawn"        // make an agent
	Operate     Action = "operate"      // start, stop, remove, attach (write) an agent
	ManageRoles Action = "manage-roles" // grant and remove roles, invite
	SeeMembers  Action = "see-members"  // see who holds what
	Unlink      Action = "unlink"       // make the hub forget the project
	Preview     Action = "preview"      // open an exposed port of an agent, from its link
)

// CLI is the actor string of the CLI and of agents spawned from it. No person is behind it on the hub: those agents are the
// administrators' to reach.
const CLI = "operator"

// Subject is a signed-in user as the rules see them.
type Subject struct {
	ID         string
	IsOperator bool
}

// Actor is the string an agent's `actor` label holds for a user.
func Actor(userID string) string { return "user:" + userID }

// UserOf returns the user id an `actor` label names, and false for the CLI, services and agents.
func UserOf(actor string) (string, bool) {
	id, ok := strings.CutPrefix(actor, "user:")
	return id, ok && id != ""
}

// Can reports whether a subject holding role may do action. creator is the `actor` label of the agent the action is about, empty when
// it is about the project.
func Can(subject Subject, role Role, action Action, creator string) bool {
	switch action {
	case View:
		return role == Overseer || role.AtLeast(User)
	case Inspect, Spawn:
		return role.AtLeast(User)
	case Operate:
		if role.AtLeast(Admin) {
			return true
		}
		return role == User && ownedBy(subject, creator)
	case ManageRoles, Unlink:
		return role == Owner
	case SeeMembers:
		return role == Owner || subject.IsOperator
	case Preview:
		return role.AtLeast(Viewer) // every role: an agent exposes a port on purpose. Overseeing is not a role
	}
	return false
}

func ownedBy(subject Subject, creator string) bool {
	id, ok := UserOf(creator)
	return ok && id == subject.ID
}

// CanAssign reports whether actor, who holds actorRole, may change target's role to newRole (None removes it). ownerless says the project
// has no valid owner. An owner changes anyone's role but cannot remove or lower themselves. An operator gives themselves any role and takes
// it back, and gives a project with no owner a new one; for anything else they are an owner first.
func CanAssign(actor Subject, actorRole Role, targetID string, newRole Role, ownerless bool) bool {
	if newRole != None && !newRole.Valid() {
		return false
	}
	self := actor.ID == targetID
	switch {
	case actorRole == Owner && !self:
		return true
	case actorRole == Owner && newRole == Owner:
		return true
	case !actor.IsOperator:
		return false
	}
	return self || ownerless && newRole == Owner
}
