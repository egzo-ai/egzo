package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Member is a user with a role on a project.
type Member struct {
	User store.User
	Role rbac.Role
}

// Members lists who holds a role on a project; an owner may see it, and an operator, who oversees every project's owners. An operator is
// listed like anybody else: for what they granted themselves.
func (h *Hub) Members(ctx context.Context, user store.User, project string) ([]Member, error) {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return nil, err
	}
	if !rbac.Can(user.Subject(), role, rbac.SeeMembers, "") {
		return nil, ErrForbidden
	}
	grants, err := h.store.GrantsOn(ctx, project)
	if err != nil {
		return nil, err
	}
	var members []Member
	for _, g := range grants {
		u, err := h.store.User(ctx, g.UserID)
		if err != nil {
			continue
		}
		members = append(members, Member{User: u, Role: g.Role})
	}
	return members, nil
}

// Candidates lists the users who hold no role on the project yet: the people an owner can still add. Disabled users are left out. Users exist
// once they have signed in.
func (h *Hub) Candidates(ctx context.Context, user store.User, project string) ([]store.User, error) {
	members, err := h.Members(ctx, user, project)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, m := range members {
		has[m.User.ID] = true
	}
	all, err := h.store.Users(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.User
	for _, u := range all {
		if !has[u.ID] && !u.Disabled() {
			out = append(out, u)
		}
	}
	return out, nil
}

// ownerless reports whether nobody who can sign in owns the project.
func (h *Hub) ownerless(ctx context.Context, project string) (bool, error) {
	owners, err := h.store.ValidOwners(ctx)
	return len(owners[project]) == 0, err
}

// SetRole grants, changes or (with rbac.None) removes a user's role on a project. An owner may, but cannot remove or lower themselves, and a
// project keeps a valid owner. An operator grants themselves a role with this, audited like any grant; a project with no owner gets a new
// one from them the same way.
func (h *Hub) SetRole(ctx context.Context, actor store.User, project, targetID string, role rbac.Role) error {
	actorRole, err := h.access(ctx, actor, project)
	if err != nil {
		return err
	}
	audit := store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: "role.set", Project: project,
		Detail: fmt.Sprintf("%s -> %q", targetID, role)}
	refuse := func(err error, why string) error {
		audit.Detail = "refused: " + why + audit.Detail
		h.record(ctx, audit)
		return err
	}
	if !rbac.Can(actor.Subject(), actorRole, rbac.SeeMembers, "") {
		return refuse(ErrForbidden, "")
	}
	target, err := h.store.User(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if target.Disabled() && role != rbac.None {
		return refuse(ErrDisabled, "a disabled user: ")
	}
	ownerless, err := h.ownerless(ctx, project)
	if err != nil {
		return err
	}
	if !rbac.CanAssign(actor.Subject(), actorRole, targetID, role, ownerless) {
		return refuse(ErrForbidden, "")
	}
	if err := h.store.SetRole(ctx, project, targetID, role); errors.Is(err, store.ErrLastOwner) {
		return refuse(ErrLastOwner, "the last owner: ")
	} else if err != nil {
		return err
	}
	audit.Allowed = true
	return h.store.Audit(ctx, audit)
}

// Users lists everyone who has signed in, disabled users included. Only an operator may.
func (h *Hub) Users(ctx context.Context, user store.User) ([]UserRow, error) {
	if err := h.requireOperator(ctx, user, "users.list", ""); err != nil {
		return nil, err
	}
	users, err := h.store.Users(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]UserRow, 0, len(users))
	for _, u := range users {
		n, err := h.store.ConnectedProjects(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, UserRow{User: u, Projects: n})
	}
	return rows, nil
}

// UserRow is a person with what their quota is measured against.
type UserRow struct {
	store.User
	Projects int // the projects they connected
}

// SetProjectQuota says how many projects a user may connect. Only an operator may. Lowering it under what they have removes nothing: it stops
// new ones. A change that changes nothing is not written to the audit log.
func (h *Hub) SetProjectQuota(ctx context.Context, actor store.User, targetID string, quota int) error {
	if err := h.requireOperator(ctx, actor, "quota.set", ""); err != nil {
		return err
	}
	if quota < 0 {
		return ErrBadQuota
	}
	target, err := h.store.User(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	changed, err := h.store.SetProjectQuota(ctx, targetID, quota)
	if err != nil || !changed {
		return err
	}
	return h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: "quota.set", Detail: fmt.Sprintf("%s (%s): %d", target.Username, target.ID, quota), Allowed: true})
}

// ErrBadQuota is returned for a quota under zero.
var ErrBadQuota = errors.New("a quota cannot be negative")

// SetOperator makes a user an operator or takes it away. Only an operator may, and the last one cannot be demoted. A change that
// changes nothing is not an error and is not written to the audit log.
func (h *Hub) SetOperator(ctx context.Context, actor store.User, targetID string, on bool) error {
	if err := h.requireOperator(ctx, actor, "operator.set", ""); err != nil {
		return err
	}
	target, err := h.store.User(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	action := "operator.revoke"
	if on {
		action = "operator.grant"
	}
	entry := store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: action, Detail: target.DisplayName() + " (" + target.ID + ")"}
	changed, err := h.store.SetOperator(ctx, targetID, on)
	if errors.Is(err, store.ErrLastOperator) {
		entry.Detail = "refused: the last operator: " + entry.Detail
		h.record(ctx, entry)
		return ErrLastOperator
	}
	if errors.Is(err, store.ErrDisabled) {
		return ErrDisabled
	}
	if err != nil {
		return err
	}
	if changed {
		entry.Allowed = true
		return h.store.Audit(ctx, entry)
	}
	return nil
}
