package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"
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
