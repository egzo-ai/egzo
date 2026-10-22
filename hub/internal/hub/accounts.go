package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// The ways people sign in (see roadmap/hub-login.md): the starter account's password, and identities at the one configured
// OpenID Connect issuer. A person is a user; a password and identities are ways to prove it. Nothing is matched by email.

var (
	// ErrBadLogin is the one answer to a password sign-in that fails, whatever the reason: no such user, a wrong password, a
	// disabled user.
	ErrBadLogin = errors.New("wrong username or password")
	// ErrSignInRefused is the answer to a single sign-on of a disabled user. It gives no reason.
	ErrSignInRefused = errors.New("sign-in refused")
	// ErrNoLocalLogin is returned for a password sign-in while local login is off.
	ErrNoLocalLogin = errors.New("signing in with a password is turned off")
	// ErrNoOperator stops the start of a hub that nobody could manage.
	ErrNoOperator = errors.New("no operator can sign in")

	ErrDisabled      = store.ErrDisabled
	ErrCannotDisable = store.ErrOperator
	ErrLastWayIn     = store.ErrLastWayIn
	ErrIssuerLinked  = store.ErrIssuerLinked
	ErrNotDisabled   = store.ErrNotDisabled
)

// SignInOptions is how people may sign in, as the hub was started.
type SignInOptions struct {
	LocalLogin    bool   // the starter account's password sign-in
	AdminPassword string // EGZO_ADMIN_PASSWORD: saved at every start when set
	Issuer        string // the one issuer trusted; empty without single sign-on
}

// SignIn is how people may sign in.
func (h *Hub) SignIn() SignInOptions {
	o := h.signIn
	o.AdminPassword = ""
	return o
}

// SetupSignIn settles the ways in at start. With local login on, the starter account exists, is enabled and is an operator, and
// its password is EGZO_ADMIN_PASSWORD when that is set (a changed password ends the password sessions), else the one it has, else
// a new one, which is returned so the caller prints it once. With it off, password sessions end. Sessions made with an identity
// of another issuer than the configured one end. A hub with local login off and no operator does not start.
func (h *Hub) SetupSignIn(ctx context.Context, o SignInOptions) (generated string, err error) {
	h.signIn = o
	if o.LocalLogin {
		admin, err := h.store.EnsureStarter(ctx)
		if err != nil {
			return "", err
		}
		hash, err := h.store.PasswordHash(ctx, admin.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		password := o.AdminPassword
		switch {
		case password != "":
			if hash != "" {
				if same, _ := auth.CheckPassword(hash, password); same {
					break
				}
				h.log.Info("the admin password changed (EGZO_ADMIN_PASSWORD): its password sessions end")
			}
			if err := h.savePassword(ctx, admin.ID, password); err != nil {
				return "", err
			}
		case hash == "":
			generated = auth.NewPassword()
			if err := h.savePassword(ctx, admin.ID, generated); err != nil {
				return "", err
			}
		}
	} else {
		if o.AdminPassword != "" {
			h.log.Warn("EGZO_ADMIN_PASSWORD is ignored: EGZO_LOCAL_LOGIN is false")
		}
		if ended, err := h.store.EndPasswordSessions(ctx); err != nil {
			return "", err
		} else if len(ended) > 0 {
			h.log.Info("local login is off: password sessions ended", "sessions", len(ended))
		}
	}
	issuers, err := h.store.EndSessionsNotAt(ctx, o.Issuer)
	if err != nil {
		return "", err
	}
	if len(issuers) > 0 {
		h.log.Warn("sessions made with another login provider ended: its accounts cannot sign in any more", "previous", strings.Join(issuers, ", "), "configured", o.Issuer)
	}
	if !o.LocalLogin {
		operators, err := h.store.CountOperators(ctx)
		if err != nil {
			return "", err
		}
		if operators == 0 {
			return "", fmt.Errorf("%w: the database has no operator and local login is off. Start with EGZO_LOCAL_LOGIN=true, sign in as admin, link %s, then turn it off", ErrNoOperator, o.Issuer)
		}
		reachable, err := h.store.OperatorsWithIdentityAt(ctx, o.Issuer)
		if err != nil {
			return "", err
		}
		if reachable == 0 {
			h.log.Error("no operator has an account at the configured login provider, so nobody can sign in as an operator. "+
				"To get in: set EGZO_LOCAL_LOGIN=true and EGZO_ADMIN_PASSWORD, restart, sign in as admin", "issuer", o.Issuer)
		}
	}
	return generated, nil
}

func (h *Hub) savePassword(ctx context.Context, userID, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = h.store.SetPassword(ctx, userID, hash)
	return err
}

// PasswordLogin signs in with a username and a password. Every failure is ErrBadLogin and costs the same time. A failure for a
// user is written to the audit log; one for a username nobody has only to the hub's log, as anyone can send as many of those as
// they like.
func (h *Hub) PasswordLogin(ctx context.Context, username, password string) (store.User, error) {
	if !h.signIn.LocalLogin {
		return store.User{}, ErrNoLocalLogin
	}
	user, err := h.store.UserByName(ctx, username)
	hash := ""
	if err == nil {
		hash, err = h.store.PasswordHash(ctx, user.ID)
	}
	ok := false
	if err == nil {
		ok, _ = auth.CheckPassword(hash, password)
	} else {
		auth.CheckNoPassword(password)
	}
	if !ok || user.Disabled() {
		if user.ID == "" {
			if len(username) > 64 {
				username = username[:64] + "..."
			}
			h.log.Info("password sign-in refused: no such user", "username", username)
		} else {
			_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "user.signin", Detail: fmt.Sprintf("refused: password for %q", user.Username)})
		}
		return store.User{}, ErrBadLogin
	}
	if err := h.store.MarkLogin(ctx, user.ID); err != nil {
		return store.User{}, err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "user.signin", Detail: "password", Allowed: true})
	return user, nil
}

// IdentityLogin signs in with an identity the provider vouched for. A new identity makes a new user with no access; a disabled
// user's is refused (ErrSignInRefused), with nothing refreshed and no user made. identityID is the identity, for the session.
func (h *Hub) IdentityLogin(ctx context.Context, issuer, subject string, p store.Profile) (user store.User, identityID string, err error) {
	if h.signIn.Issuer == "" || issuer != h.signIn.Issuer {
		return store.User{}, "", fmt.Errorf("the identity is from %q, not the configured issuer %q", issuer, h.signIn.Issuer)
	}
	user, identityID, created, err := h.store.LoginIdentity(ctx, issuer, subject, p)
	if errors.Is(err, store.ErrDisabled) {
		_ = h.store.Audit(ctx, store.AuditEntry{Actor: "hub", Action: "user.signin", Detail: fmt.Sprintf("refused: a disabled user, at %s as %s", issuer, subject)})
		return store.User{}, "", ErrSignInRefused
	}
	if err != nil {
		return store.User{}, "", err
	}
	if created {
		_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "user.create", Detail: fmt.Sprintf("%s, at %s", user.Username, issuer), Allowed: true})
	}
	return user, identityID, nil
}

// LinkIdentity adds an identity, which the user just proved control of at the provider, to the user.
func (h *Hub) LinkIdentity(ctx context.Context, user store.User, issuer, subject string, p store.Profile) (store.Identity, error) {
	if h.signIn.Issuer == "" || issuer != h.signIn.Issuer {
		return store.Identity{}, fmt.Errorf("the identity is from %q, not the configured issuer %q", issuer, h.signIn.Issuer)
	}
	entry := store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "identity.link", Detail: fmt.Sprintf("%s at %s", orSubject(p.Email, subject), issuer)}
	identity, err := h.store.LinkIdentity(ctx, user.ID, issuer, subject, p)
	if err != nil {
		entry.Detail = "refused: " + err.Error() + ": " + entry.Detail
		_ = h.store.Audit(ctx, entry)
		return store.Identity{}, err
	}
	entry.Allowed = true
	return identity, h.store.Audit(ctx, entry)
}

// UnlinkIdentity removes one of the user's own identities and ends the sessions made with it; the ended sessions are returned.
// The last way to sign in cannot be removed.
func (h *Hub) UnlinkIdentity(ctx context.Context, user store.User, identityID string) ([]string, error) {
	identity, ended, err := h.store.UnlinkIdentity(ctx, user.ID, identityID, h.signIn.Issuer, h.signIn.LocalLogin)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return ended, h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "identity.unlink",
		Detail: fmt.Sprintf("%s at %s", orSubject(identity.Email, identity.Subject), identity.Issuer), Allowed: true})
}

// RemoveIdentity takes an identity from a disabled user, so it can be linked to somebody else. Only an operator may.
func (h *Hub) RemoveIdentity(ctx context.Context, actor store.User, userID, identityID string) error {
	if err := h.requireOperator(ctx, actor, "identity.remove", ""); err != nil {
		return err
	}
	identity, err := h.store.RemoveIdentity(ctx, userID, identityID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	target, _ := h.store.User(ctx, userID)
	return h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: "identity.remove",
		Detail: fmt.Sprintf("%s at %s, from %s (%s)", orSubject(identity.Email, identity.Subject), identity.Issuer, target.Username, userID), Allowed: true})
}

// SetDisabled disables or enables a user. Only an operator may, and an operator cannot be disabled (demote them first). Disabling
// ends the user's sessions, which are returned so their terminals can be closed. An unchanged state is not an error and is not
// written to the audit log.
func (h *Hub) SetDisabled(ctx context.Context, actor store.User, targetID string, on bool) ([]string, error) {
	action := "user.enable"
	if on {
		action = "user.disable"
	}
	if err := h.requireOperator(ctx, actor, action, ""); err != nil {
		return nil, err
	}
	target, err := h.store.User(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	entry := store.AuditEntry{Actor: rbac.Actor(actor.ID), Action: action, Detail: target.Username + " (" + target.ID + ")"}
	changed, ended, err := h.store.SetDisabled(ctx, targetID, on)
	if errors.Is(err, store.ErrOperator) {
		entry.Detail = "refused: an operator: " + entry.Detail
		_ = h.store.Audit(ctx, entry)
		return nil, ErrCannotDisable
	}
	if err != nil || !changed {
		return nil, err
	}
	entry.Allowed = true
	return ended, h.store.Audit(ctx, entry)
}

func orSubject(email, subject string) string {
	if email != "" {
		return email
	}
	return subject
}
