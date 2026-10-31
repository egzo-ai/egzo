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

// A link code is how a project comes to exist on the hub: an operator makes one, and whoever holds it can register one project, as that
// operator. The hub keeps its hash, so a copy of the database holds no live code.

// LinkCodePrefix starts every code, for the secret scanners to find one that was committed.
const LinkCodePrefix = "egzo-link-"

// LinkCodeLife is how long a code stays good.
const LinkCodeLife = 15 * time.Minute

// ErrBadCode is returned for a code that is wrong, expired, used, cancelled, or made by someone who may no longer make one. Why is
// not said: a code that fails is worth nothing to anyone, whichever it was.
var ErrBadCode = errors.New("no such link code")

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

func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return sum[:]
}

// newCode is 128 random bits.
func newCode() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return LinkCodePrefix + strings.ToLower(codeEncoding.EncodeToString(raw))
}

// CreateLinkCode makes a code. What it returns is the only time the code exists in the clear.
func (s *Store) CreateLinkCode(ctx context.Context, userID string, now time.Time) (LinkCode, string, error) {
	code := newCode()
	c := LinkCode{ID: newID("c", 6), CreatedBy: userID, Created: now, Expires: now.Add(LinkCodeLife)}
	_, err := s.db.ExecContext(ctx, `INSERT INTO link_codes (id, hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		c.ID, hashCode(code), userID, now.UnixMilli(), c.Expires.UnixMilli())
	return c, code, err
}

// LinkCodes lists the codes that are still good, the newest first.
func (s *Store) LinkCodes(ctx context.Context, now time.Time) ([]LinkCode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_by, created_at, expires_at FROM link_codes WHERE expires_at > ? ORDER BY created_at DESC, id`, now.UnixMilli())
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

// CancelLinkCode takes a code back.
func (s *Store) CancelLinkCode(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM link_codes WHERE id = ?`, id)
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
// allowed to: one who was disabled or is no longer an operator made a code that is worth nothing.
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
	var allowed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND is_operator = 1 AND disabled_at IS NULL`, r.Owner).Scan(&allowed); err != nil {
		return Registration{}, err
	}
	if allowed == 0 {
		return Registration{}, ErrBadCode
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
