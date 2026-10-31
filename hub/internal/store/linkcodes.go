package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// A link code is how a project comes to exist on the hub: a person who may connect one makes it, and whoever holds it can register one
// project, as that person. The hub keeps its hash, so a copy of the database holds no live code.

// LinkCodePrefix starts every code, for the secret scanners to find one that was committed.
const LinkCodePrefix = "egzo-link-"

// LinkCodeLife is how long a code stays good.
const LinkCodeLife = 15 * time.Minute

// ErrBadCode is returned for a code that is wrong, expired, used, cancelled, or made by someone who may no longer make one. Why is
// not said: a code that fails is worth nothing to anyone, whichever it was.
var ErrBadCode = errors.New("no such link code")

// ErrOverQuota is returned for a code whose maker has as many projects as their quota allows. The code is left as it was: it is good again
// if they make room for it.
var ErrOverQuota = errors.New("the project quota is used up")

// ErrKeyTaken is returned when another connection registered the same key first. The code is left as it was.
var ErrKeyTaken = errors.New("the key is registered already")

// LinkCode is a code as it is listed: everything but the code.
type LinkCode struct {
	ID        string
	CreatedBy string
	Created   time.Time
	Expires   time.Time
}

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// hashCode is how a code is kept: it is 128 random bits, so a plain hash holds it.
func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return sum[:]
}

// newCode is 128 random bits after a prefix.
func newCode(prefix string) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(codeEncoding.EncodeToString(raw))
}

// CreateLinkCode makes a code. What it returns is the only time the code exists in the clear.
func (s *Store) CreateLinkCode(ctx context.Context, userID string, now time.Time) (LinkCode, string, error) {
	code := newCode(LinkCodePrefix)
	c := LinkCode{ID: newID("c", 6), CreatedBy: userID, Created: now, Expires: now.Add(LinkCodeLife)}
	_, err := s.db.ExecContext(ctx, `INSERT INTO link_codes (id, hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		c.ID, hashCode(code), userID, now.UnixMilli(), c.Expires.UnixMilli())
	return c, code, err
}

// LinkCodes lists the codes that are still good, the newest first: those of one person, or everyone's when userID is empty.
func (s *Store) LinkCodes(ctx context.Context, userID string, now time.Time) ([]LinkCode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_by, created_at, expires_at FROM link_codes WHERE expires_at > ? AND (? = '' OR created_by = ?)
		ORDER BY created_at DESC, id`, now.UnixMilli(), userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkCode
	for rows.Next() {
		var c LinkCode
		var created, expires int64
		if err := rows.Scan(&c.ID, &c.CreatedBy, &created, &expires); err != nil {
			return nil, err
		}
		c.Created, c.Expires = time.UnixMilli(created), time.UnixMilli(expires)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CancelLinkCode takes a code back: one person's, or anyone's when userID is empty.
func (s *Store) CancelLinkCode(ctx context.Context, id, userID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM link_codes WHERE id = ? AND (? = '' OR created_by = ?)`, id, userID, userID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeLinkCodes drops the codes that expired.
func (s *Store) PurgeLinkCodes(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM link_codes WHERE expires_at <= ?`, now.UnixMilli())
	return err
}

// RedeemLinkCode spends a code on a link's key, in one transaction: the code goes, and the project is there, approved and owned by whoever
// made the code. The delete comes first, so of two links that hold one code exactly one finds it. The person who made it must still be
// allowed to: a disabled person made a code that is worth nothing, and one with no room left in their quota gets ErrOverQuota, with Owner set
// in what is returned. An operator is not held to a quota. Quota and count are read in the transaction that adds the project.
func (s *Store) RedeemLinkCode(ctx context.Context, code string, r Registration, now time.Time) (Registration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `DELETE FROM link_codes WHERE hash = ? AND expires_at > ? RETURNING created_by`, hashCode(code), now.UnixMilli()).Scan(&r.Owner)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrBadCode
	}
	if err != nil {
		return Registration{}, err
	}
	var operator, quota, have int
	err = tx.QueryRowContext(ctx, `SELECT is_operator, project_quota, (SELECT COUNT(*) FROM projects WHERE owner = u.id AND state = ?) FROM users u
		WHERE id = ? AND disabled_at IS NULL`, hublink.StateApproved, r.Owner).Scan(&operator, &quota, &have)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrBadCode
	}
	if err != nil {
		return Registration{}, err
	}
	if operator == 0 && have >= quota {
		return r, ErrOverQuota
	}
	templates, err := json.Marshal(append([]string{}, r.Templates...))
	if err != nil {
		return Registration{}, err
	}
	r.ID, r.State, r.Created, r.Decided = newID("p", 6), hublink.StateApproved, now, now
	result, err := tx.ExecContext(ctx, `INSERT INTO projects (id, key, name, host, release, templates, state, owner, created_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		r.ID, r.Key, r.Name, r.Host, r.Release, string(templates), r.State, r.Owner, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Registration{}, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return Registration{}, ErrKeyTaken
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants (project, user_id, role) VALUES (?, ?, ?)`, r.ID, r.Owner, string(rbac.Owner)); err != nil {
		return Registration{}, err
	}
	return r, tx.Commit()
}
