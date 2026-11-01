package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// An invite is how a person comes to the hub: signing in is not enough, and an account is made only by spending one. It is a code, good once,
// that carries what it grants: a role on a project, from that project's owner, or the flags on a user, from an operator. Whoever holds it can
// spend it: it is matched to nobody, by email or otherwise. The hub keeps its hash, like a link code's.

// InvitePrefix starts every code, for the secret scanners to find one that was committed.
const InvitePrefix = "egzo-invite-"

// InviteLife is how long an invite stays good: long enough for a person to find the message it came in.
const InviteLife = 7 * 24 * time.Hour

// ErrBadInvite is returned for a code that is wrong, expired, used, revoked, or made by someone who may no longer make it. Why is not said.
var ErrBadInvite = errors.New("no such invite")

// ErrNoInvite is returned for a sign-in of a person the hub does not know, who brought no invite.
var ErrNoInvite = errors.New("an account here takes an invite")

// ErrUsernameTaken is returned when a person chooses a name that somebody has.
var ErrUsernameTaken = errors.New("that username is taken")

// Invite is an invite as it is listed: everything but the code. With a Project it grants Role on it; without one, Operator and ProjectQuota.
type Invite struct {
	ID           string
	CreatedBy    string
	Created      time.Time
	Expires      time.Time
	Project      string
	Role         rbac.Role
	Operator     bool
	ProjectQuota int
}

const inviteColumns = `id, created_by, created_at, expires_at, COALESCE(project, ''), role, operator, project_quota`

func scanInvite(row scanner) (Invite, error) {
	var i Invite
	var created, expires int64
	var operator int
	if err := row.Scan(&i.ID, &i.CreatedBy, &created, &expires, &i.Project, &i.Role, &operator, &i.ProjectQuota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrBadInvite
		}
		return Invite{}, err
	}
	i.Created, i.Expires, i.Operator = time.UnixMilli(created), time.UnixMilli(expires), operator == 1
	return i, nil
}

// CreateInvite makes an invite from what i says it grants. What it returns is the only time the code exists in the clear.
func (s *Store) CreateInvite(ctx context.Context, i Invite, now time.Time) (Invite, string, error) {
	code := newCode(InvitePrefix)
	i.ID, i.Created, i.Expires = newID("n", 6), now, now.Add(InviteLife)
	var project any
	if i.Project != "" {
		project = i.Project
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO invites (id, hash, created_by, created_at, expires_at, project, role, operator, project_quota)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, i.ID, hashCode(code), i.CreatedBy, now.UnixMilli(), i.Expires.UnixMilli(), project, string(i.Role), boolInt(i.Operator), i.ProjectQuota)
	return i, code, err
}

// Invites lists the invites that are still good, the newest first: those of one project, or every one when project is empty.
func (s *Store) Invites(ctx context.Context, project string, now time.Time) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE expires_at > ? AND (? = '' OR project = ?) ORDER BY created_at DESC, id`,
		now.UnixMilli(), project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// InviteByID reads an invite for who may revoke it.
func (s *Store) InviteByID(ctx context.Context, id string) (Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE id = ?`, id))
}

// InviteByCode reads an invite that could be spent now, to show what it grants. Its code stays good.
func (s *Store) InviteByCode(ctx context.Context, code string, now time.Time) (Invite, error) {
	i, err := scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE hash = ? AND expires_at > ?`, hashCode(code), now.UnixMilli()))
	if err != nil {
		return Invite{}, err
	}
	return i, inviteStands(ctx, s.db, i)
}

// RevokeInvite takes an invite back.
func (s *Store) RevokeInvite(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeInvites drops the invites that expired.
func (s *Store) PurgeInvites(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE expires_at <= ?`, now.UnixMilli())
	return err
}

// RedeemInvite spends a code on a user who exists.
func (s *Store) RedeemInvite(ctx context.Context, code, userID string, now time.Time) (Invite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, err
	}
	defer tx.Rollback()
	i, err := redeem(ctx, tx, code, userID, now)
	if err != nil {
		return Invite{}, err
	}
	return i, tx.Commit()
}

// RegisterLocal spends a code on a new account with a password, in one transaction: an invite that fails makes nobody.
func (s *Store) RegisterLocal(ctx context.Context, code, username, passwordHash string, now time.Time) (User, Invite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, Invite{}, err
	}
	defer tx.Rollback()
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&taken); err != nil {
		return User{}, Invite{}, err
	}
	if taken > 0 {
		return User{}, Invite{}, ErrUsernameTaken
	}
	u, err := insertNamed(ctx, tx, username, false, now)
	if err != nil {
		return User{}, Invite{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_passwords (user_id, hash, updated_at) VALUES (?, ?, ?)`, u.ID, passwordHash, now.Unix()); err != nil {
		return User{}, Invite{}, err
	}
	i, err := redeem(ctx, tx, code, u.ID, now)
	if err != nil {
		return User{}, Invite{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, Invite{}, err
	}
	u, err = s.User(ctx, u.ID)
	return u, i, err
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inviteStands checks that whoever made an invite may still make it, and that what it grants is still there: an invite of a person who was
// disabled, lost the project or stopped being an operator is worth nothing, like a link code. dropFallenInvites deletes those in the
// transaction that takes the right away; this is what makes that the rule for a code in somebody's hand.
func inviteStands(ctx context.Context, q querier, i Invite) error {
	var allowed int
	var err error
	if i.Project != "" {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants g JOIN users u ON u.id = g.user_id JOIN projects p ON p.id = g.project
			WHERE g.project = ? AND g.user_id = ? AND g.role = ? AND u.disabled_at IS NULL AND p.state = ?`, i.Project, i.CreatedBy, string(rbac.Owner), hublink.StateApproved).Scan(&allowed)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND is_operator = 1 AND disabled_at IS NULL`, i.CreatedBy).Scan(&allowed)
	}
	if err != nil {
		return err
	}
	if allowed == 0 {
		return ErrBadInvite
	}
	return nil
}

// dropFallenInvites deletes what inviteStands would refuse, in the transaction that made it so: giving the right back does not bring an invite
// back, and no list shows one as good. The audit log says so, in the same transaction.
func dropFallenInvites(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `DELETE FROM invites WHERE
		(project IS NOT NULL AND NOT EXISTS (SELECT 1 FROM grants g JOIN users u ON u.id = g.user_id JOIN projects p ON p.id = g.project
			WHERE g.project = invites.project AND g.user_id = invites.created_by AND g.role = ? AND u.disabled_at IS NULL AND p.state = ?))
		OR (project IS NULL AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = invites.created_by AND u.is_operator = 1 AND u.disabled_at IS NULL))
		RETURNING `+inviteColumns, string(rbac.Owner), hublink.StateApproved)
	if err != nil {
		return err
	}
	var dropped []Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			rows.Close()
			return err
		}
		dropped = append(dropped, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, i := range dropped {
		if err := audit(ctx, tx, AuditEntry{Actor: "hub", Action: "invite.revoke", Project: i.Project, Detail: i.ID + ": its maker can no longer give it", Allowed: true}); err != nil {
			return err
		}
	}
	return nil
}

// redeem spends a code on a user. The delete comes first, so of two people who hold one code exactly one finds it. What it grants is only
// ever added: a role is raised and never lowered, a quota only grows, so an invite cannot take from whoever spends it.
func redeem(ctx context.Context, tx *sql.Tx, code, userID string, now time.Time) (Invite, error) {
	i, err := scanInvite(tx.QueryRowContext(ctx, `DELETE FROM invites WHERE hash = ? AND expires_at > ? RETURNING `+inviteColumns, hashCode(code), now.UnixMilli()))
	if err != nil {
		return Invite{}, err
	}
	if err := inviteStands(ctx, tx, i); err != nil {
		return Invite{}, err
	}
	if i.Project == "" {
		_, err := tx.ExecContext(ctx, `UPDATE users SET is_operator = MAX(is_operator, ?), project_quota = MAX(project_quota, ?) WHERE id = ?`, boolInt(i.Operator), i.ProjectQuota, userID)
		return i, err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT role FROM grants WHERE project = ? AND user_id = ?`, i.Project, userID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Invite{}, err
	}
	if rbac.Role(current).AtLeast(i.Role) {
		return i, nil
	}
	return i, setGrant(ctx, tx, i.Project, userID, i.Role)
}
