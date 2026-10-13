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

// Members lists who holds a role on a project; only an owner may see it.
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
		if err != nil {
			continue
		}
		members = append(members, Member{User: u, Role: g.Role})
	}
	return members, nil
}

// Candidates lists the users who hold no role on the project yet: the people an owner can still invite. Users exist once they have logged in.
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
		if !has[u.ID] {
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
	if !rbac.Can(actor.Subject(), actorRole, rbac.ManageRoles, "") || !rbac.CanAssign(actorRole, actor.ID, targetID, role) {
		audit.Detail = "refused: " + audit.Detail
		_ = h.store.Audit(ctx, audit)
		return ErrForbidden
	}
	if _, err := h.store.User(ctx, targetID); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if err := h.store.SetRole(ctx, engine, project, targetID, role); err != nil {
		return err
	}
	audit.Allowed = true
	return h.store.Audit(ctx, audit)
}
