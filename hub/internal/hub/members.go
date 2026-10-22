package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Member is a user with a role on a project. An operator is listed too, as owner with Operator set: they hold the project without
// a grant, so their row cannot be changed here.
type Member struct {
	User     store.User
	Role     rbac.Role
	Operator bool
}

// Members lists who holds a role on a project, then the operators; only an owner may see it.
func (h *Hub) Members(ctx context.Context, user store.User, engine, project string) ([]Member, error) {
	role, err := h.access(ctx, user, engine, project)
	if err != nil {
		return nil, err
	}
	if !rbac.Can(user.Subject(), role, rbac.ManageRoles, "") {
		return nil, ErrForbidden
	}
	grants, err := h.store.GrantsOn(ctx, engine, project)
	if err != nil {
		return nil, err
	}
	var members []Member
	for _, g := range grants {
		u, err := h.store.User(ctx, g.UserID)
		if err != nil || u.IsOperator {
			continue // an operator's own grant, if one is left over, is not what gives them the project
		}
		members = append(members, Member{User: u, Role: g.Role})
	}
	all, err := h.store.Users(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range all {
		if u.IsOperator {
			members = append(members, Member{User: u, Role: rbac.Owner, Operator: true})
		}
	}
	return members, nil
}

// Candidates lists the users who hold no role on the project yet: the people an owner can still invite. Operators hold every
// project, so they are not among them, and disabled users are left out. Users exist once they have signed in.
func (h *Hub) Candidates(ctx context.Context, user store.User, engine, project string) ([]store.User, error) {
	members, err := h.Members(ctx, user, engine, project)
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

// SetRole grants, changes or (with rbac.None) removes a user's role on a project. Only an owner may; an owner cannot remove or
// lower themselves.
func (h *Hub) SetRole(ctx context.Context, actor store.User, engine, project, targetID string, role rbac.Role) error {
	actorRole, err := h.access(ctx, actor, engine, project)
	if err != nil {
		return err
	}
	audit := store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: "role.set", Engine: engine, Project: project,
		Detail: fmt.Sprintf("%s -> %q", targetID, role)}
	refuse := func(err error, why string) error {
		audit.Detail = "refused: " + why + audit.Detail
		_ = h.store.Audit(ctx, audit)
		return err
	}
	if !rbac.Can(actor.Subject(), actorRole, rbac.ManageRoles, "") {
		return refuse(ErrForbidden, "")
	}
	target, err := h.store.User(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if target.IsOperator {
		return refuse(ErrOperatorAccess, "an operator holds every project: ")
	}
	if target.Disabled() && role != rbac.None {
		return refuse(ErrDisabled, "a disabled user: ")
	}
	if !rbac.CanAssign(actorRole, actor.ID, targetID, role) {
		return refuse(ErrForbidden, "")
	}
	if err := h.store.SetRole(ctx, engine, project, targetID, role); err != nil {
		return err
	}
	audit.Allowed = true
	return h.store.Audit(ctx, audit)
}

// Users lists everyone who has signed in, disabled users included. Only an operator may.
func (h *Hub) Users(ctx context.Context, user store.User) ([]store.User, error) {
	if err := h.requireOperator(ctx, user, "users.list", ""); err != nil {
		return nil, err
	}
	return h.store.Users(ctx)
}

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
		_ = h.store.Audit(ctx, entry)
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
