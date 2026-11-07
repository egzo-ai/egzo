package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// A bridge links a person's chat account to their hub user with a code: the bridge asks for one for the account, shows the person the
// address it goes with, and the person confirms it signed in on the hub. The hub then knows whose account it is: a bridge names the
// account a message comes from, never the hub user.

// BridgeCodePrefix starts every bridge code.
const BridgeCodePrefix = "egzo-chat-"

// BridgeCodeLife is how long a bridge code stays good.
const BridgeCodeLife = 15 * time.Minute

// BridgeAccount is a chat account linked to a hub user.
type BridgeAccount struct {
	Bridge       string
	ExternalID   string
	ExternalName string
	UserID       string
	Username     string // the hub user's
	Linked       time.Time
}

// BridgeCode is what a code is for, without the code.
type BridgeCode struct {
	Bridge       string
	ExternalID   string
	ExternalName string
	Expires      time.Time
}

// CreateBridgeCode makes a code for a chat account. What it returns is the only time the code exists in the clear.
func (s *Store) CreateBridgeCode(ctx context.Context, bridge, externalID, externalName string, now time.Time) (string, time.Time, error) {
	code := newCode(BridgeCodePrefix)
	expires := now.Add(BridgeCodeLife)
	_, err := s.db.ExecContext(ctx, `INSERT INTO bridge_codes (hash, bridge, external_id, external_name, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hashCode(code), bridge, externalID, externalName, expires.UnixMilli())
	return code, expires, err
}

// BridgeCodeFor says what a code that is still good is for.
func (s *Store) BridgeCodeFor(ctx context.Context, code string, now time.Time) (BridgeCode, error) {
	var c BridgeCode
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT bridge, external_id, external_name, expires_at FROM bridge_codes WHERE hash = ? AND expires_at > ?`,
		hashCode(code), now.UnixMilli()).Scan(&c.Bridge, &c.ExternalID, &c.ExternalName, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrBadCode
	}
	c.Expires = time.UnixMilli(expires)
	return c, err
}

// ConfirmBridgeCode spends a code: the account becomes the user's, whoever's it was before.
func (s *Store) ConfirmBridgeCode(ctx context.Context, code, userID string, now time.Time) (BridgeAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BridgeAccount{}, err
	}
	defer tx.Rollback()
	var a BridgeAccount
	err = tx.QueryRowContext(ctx, `DELETE FROM bridge_codes WHERE hash = ? AND expires_at > ? RETURNING bridge, external_id, external_name`,
		hashCode(code), now.UnixMilli()).Scan(&a.Bridge, &a.ExternalID, &a.ExternalName)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrBadCode
	}
	if err != nil {
		return a, err
	}
	a.UserID, a.Linked = userID, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_accounts (bridge, external_id, external_name, user_id, linked_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (bridge, external_id) DO UPDATE SET external_name = excluded.external_name, user_id = excluded.user_id, linked_at = excluded.linked_at`,
		a.Bridge, a.ExternalID, a.ExternalName, userID, now.UnixMilli()); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

const bridgeAccountSelect = `SELECT b.bridge, b.external_id, b.external_name, b.user_id, u.username, b.linked_at FROM bridge_accounts b JOIN users u ON u.id = b.user_id `

func scanBridgeAccounts(rows *sql.Rows, err error) ([]BridgeAccount, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BridgeAccount
	for rows.Next() {
		var a BridgeAccount
		var linked int64
		if err := rows.Scan(&a.Bridge, &a.ExternalID, &a.ExternalName, &a.UserID, &a.Username, &linked); err != nil {
			return nil, err
		}
		a.Linked = time.UnixMilli(linked)
		out = append(out, a)
	}
	return out, rows.Err()
}

// BridgeAccounts lists the accounts linked through one bridge.
func (s *Store) BridgeAccounts(ctx context.Context, bridge string) ([]BridgeAccount, error) {
	return scanBridgeAccounts(s.db.QueryContext(ctx, bridgeAccountSelect+`WHERE b.bridge = ? ORDER BY b.linked_at`, bridge))
}

// AllBridgeAccounts lists every linked account, for the operator's list of users.
func (s *Store) AllBridgeAccounts(ctx context.Context) ([]BridgeAccount, error) {
	return scanBridgeAccounts(s.db.QueryContext(ctx, bridgeAccountSelect+`ORDER BY b.bridge, b.linked_at`))
}

// BridgeAccountsOf lists a user's linked accounts.
func (s *Store) BridgeAccountsOf(ctx context.Context, userID string) ([]BridgeAccount, error) {
	return scanBridgeAccounts(s.db.QueryContext(ctx, bridgeAccountSelect+`WHERE b.user_id = ? ORDER BY b.bridge, b.linked_at`, userID))
}

// BridgeAccountOf is the account a bridge names, ErrNotFound when nobody linked it.
func (s *Store) BridgeAccountOf(ctx context.Context, bridge, externalID string) (BridgeAccount, error) {
	list, err := scanBridgeAccounts(s.db.QueryContext(ctx, bridgeAccountSelect+`WHERE b.bridge = ? AND b.external_id = ?`, bridge, externalID))
	if err != nil {
		return BridgeAccount{}, err
	}
	if len(list) == 0 {
		return BridgeAccount{}, ErrNotFound
	}
	return list[0], nil
}

// UnlinkBridgeAccount removes one of a user's accounts. It reports whether there was one.
func (s *Store) UnlinkBridgeAccount(ctx context.Context, userID, bridge, externalID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM bridge_accounts WHERE user_id = ? AND bridge = ? AND external_id = ?`, userID, bridge, externalID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// PurgeBridgeCodes drops the codes that expired.
func (s *Store) PurgeBridgeCodes(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bridge_codes WHERE expires_at <= ?`, now.UnixMilli())
	return err
}
