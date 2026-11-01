package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Invites are how people get in (store/invites.go): a project's owner invites to a role on it, an operator to the flags on a user. Whoever
// holds the code spends it, by signing in with it or by making an account.

var (
	// ErrBadInvite is the one answer to a code that cannot be spent, whatever the reason.
	ErrBadInvite = store.ErrBadInvite
	// ErrUsernameTaken and ErrBadUsername refuse a name chosen for a new account.
	ErrUsernameTaken = store.ErrUsernameTaken
	ErrBadUsername   = errors.New("a username is lowercase letters, digits, dots, dashes and underscores, and not a reserved name")
	// ErrWeakPassword refuses a password too short to be one.
	ErrWeakPassword = fmt.Errorf("a password has at least %d characters", minPassword)
)

// minPassword is the length of the shortest password a person may choose.
const minPassword = 12

// maxPassword bounds what the hub hashes for somebody it does not know yet.
const maxPassword = 1024

// InviteView is an invite with what it takes to show it: never the code.
type InviteView struct {
	store.Invite
	CreatorName string
	ProjectName string
}

func (h *Hub) inviteView(ctx context.Context, i store.Invite) InviteView {
	view := InviteView{Invite: i}
	if creator, err := h.store.User(ctx, i.CreatedBy); err == nil {
		view.CreatorName = creator.Username
	}
	if i.Project != "" {
		if reg, err := h.store.Registration(ctx, i.Project); err == nil {
			view.ProjectName = reg.Name
		}
	}
	return view
}

func (h *Hub) inviteViews(ctx context.Context, list []store.Invite) []InviteView {
	views := make([]InviteView, 0, len(list))
	for _, i := range list {
		views = append(views, h.inviteView(ctx, i))
	}
	return views
}

// InviteToProject makes an invite to a role on a project. What it returns is the only time the code is seen. Only its owner may: an operator
// who is not grants themselves the role first.
func (h *Hub) InviteToProject(ctx context.Context, user store.User, project string, role rbac.Role) (store.Invite, string, error) {
	if !role.Valid() {
		return store.Invite{}, "", ErrBadRole
	}
	if err := h.require(ctx, user, project, rbac.ManageRoles, "invite.create"); err != nil {
		return store.Invite{}, "", err
	}
	return h.createInvite(ctx, user, store.Invite{CreatedBy: user.ID, Project: project, Role: role})
}

// ErrBadRole is returned for what is not a role that can be granted.
var ErrBadRole = errors.New("that is not a role")

// InviteToHub makes an invite that gives a person an account and the flags. Only an operator may.
func (h *Hub) InviteToHub(ctx context.Context, user store.User, operator bool, quota int) (store.Invite, string, error) {
	if err := h.requireOperator(ctx, user, "invite.create", ""); err != nil {
		return store.Invite{}, "", err
	}
	if quota < 0 {
		return store.Invite{}, "", ErrBadQuota
	}
	return h.createInvite(ctx, user, store.Invite{CreatedBy: user.ID, Operator: operator, ProjectQuota: quota})
}

func (h *Hub) createInvite(ctx context.Context, user store.User, i store.Invite) (store.Invite, string, error) {
	made, code, err := h.store.CreateInvite(ctx, i, time.Now())
	if err != nil {
		return store.Invite{}, "", err
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "invite.create", Project: i.Project, Detail: made.ID + ": " + grantsOf(i), Allowed: true})
	return made, code, nil
}

// grantsOf says what an invite gives.
func grantsOf(i store.Invite) string {
	if i.Project != "" {
		return string(i.Role)
	}
	return fmt.Sprintf("operator %t, project quota %d", i.Operator, i.ProjectQuota)
}

// Invites lists the invites that are still good, of every project and of the hub. Only an operator may.
func (h *Hub) Invites(ctx context.Context, user store.User) ([]InviteView, error) {
	if err := h.requireOperator(ctx, user, "invites.list", ""); err != nil {
		return nil, err
	}
	list, err := h.store.Invites(ctx, "", time.Now())
	if err != nil {
		return nil, err
	}
	return h.inviteViews(ctx, list), nil
}

// ProjectInvites lists the invites to a project that are still good, for whoever may see its members.
func (h *Hub) ProjectInvites(ctx context.Context, user store.User, project string) ([]InviteView, error) {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return nil, err
	}
	if !rbac.Can(user.Subject(), role, rbac.SeeMembers, "") {
		return nil, ErrForbidden
	}
	list, err := h.store.Invites(ctx, project, time.Now())
	if err != nil {
		return nil, err
	}
	return h.inviteViews(ctx, list), nil
}

// RevokeInvite takes an invite back: an operator's right on any, an owner's on those of their project.
func (h *Hub) RevokeInvite(ctx context.Context, user store.User, id string) error {
	i, err := h.store.InviteByID(ctx, id)
	if errors.Is(err, store.ErrBadInvite) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !user.IsOperator {
		if i.Project == "" {
			return ErrNotFound
		}
		if err := h.require(ctx, user, i.Project, rbac.ManageRoles, "invite.revoke"); err != nil {
			return err
		}
	}
	if err := h.store.RevokeInvite(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "invite.revoke", Project: i.Project, Detail: id, Allowed: true})
}

// PeekInvite says what a code grants, to whoever holds it: it is theirs to read before they spend it.
func (h *Hub) PeekInvite(ctx context.Context, code string) (InviteView, error) {
	i, err := h.store.InviteByCode(ctx, code, time.Now())
	if err != nil {
		return InviteView{}, err
	}
	return h.inviteView(ctx, i), nil
}

// AcceptInvite spends a code on the person signed in.
func (h *Hub) AcceptInvite(ctx context.Context, user store.User, code string) (InviteView, error) {
	i, err := h.store.RedeemInvite(ctx, code, user.ID, time.Now())
	if err != nil {
		return InviteView{}, err
	}
	h.auditRedeemed(ctx, user, i)
	return h.inviteView(ctx, i), nil
}

// RegisterLocal makes an account with a password from an invite. The code is read before anything else: a stranger neither learns which names
// are taken nor gets a password hashed.
func (h *Hub) RegisterLocal(ctx context.Context, code, username, password string) (store.User, error) {
	if !h.signIn.LocalLogin {
		return store.User{}, ErrNoLocalLogin
	}
	if _, err := h.store.InviteByCode(ctx, code, time.Now()); err != nil {
		return store.User{}, err
	}
	if !store.ValidUsername(username) {
		return store.User{}, ErrBadUsername
	}
	if len(password) < minPassword || len(password) > maxPassword {
		return store.User{}, ErrWeakPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	user, i, err := h.store.RegisterLocal(ctx, code, username, hash, time.Now())
	if err != nil {
		return store.User{}, err
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "user.create", Detail: user.Username + ", with a password", Allowed: true})
	h.auditRedeemed(ctx, user, i)
	return user, nil
}

func (h *Hub) auditRedeemed(ctx context.Context, user store.User, i store.Invite) {
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "invite.redeem", Project: i.Project, Detail: i.ID + ": " + grantsOf(i), Allowed: true})
}
