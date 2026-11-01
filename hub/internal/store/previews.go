// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// A person signs in to a preview through the hub, by redirects: the hub's cookie never reaches a preview's host. A ticket carries the sign-in
// across, once, within seconds. It leaves a preview session behind, which is what the preview's cookie stands for. Both belong to the hub session
// they came from, so they end with it: signing out, being disabled, and everything else that ends a session.

// NewPreviewTicket makes a ticket for one preview, and for a path of it to go to.
func (s *Store) NewPreviewTicket(ctx context.Context, sessionHash, project, preview, path string, ttl time.Duration) (string, error) {
	token := newID("", 32)
	_, err := s.db.ExecContext(ctx, `INSERT INTO preview_tickets (token_hash, session_hash, project, preview, path, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		HashToken(token), sessionHash, project, preview, path, time.Now().Add(ttl).Unix())
	return token, err
}

// SpendPreviewTicket spends a ticket on the preview it was made for, and returns the path and a preview session's token. A ticket works once.
func (s *Store) SpendPreviewTicket(ctx context.Context, ticket, project, preview string) (path, token string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var session string
	err = tx.QueryRowContext(ctx, `DELETE FROM preview_tickets WHERE token_hash = ? AND project = ? AND preview = ? AND expires_at > ? RETURNING session_hash, path`,
		HashToken(ticket), project, preview, time.Now().Unix()).Scan(&session, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	token = newID("", 32)
	if _, err := tx.ExecContext(ctx, `INSERT INTO preview_sessions (token_hash, session_hash, project, preview) VALUES (?, ?, ?, ?)`, HashToken(token), session, project, preview); err != nil {
		return "", "", err
	}
	return path, token, tx.Commit()
}

// PreviewVisitor reads a preview's cookie: who it is, the hub session it came from, and when that ends. It holds for the one preview it was made for.
func (s *Store) PreviewVisitor(ctx context.Context, token, project, preview string) (User, string, time.Time, error) {
	if token == "" {
		return User{}, "", time.Time{}, ErrNotFound
	}
	var session string
	var expires int64
	now := time.Now().Unix()
	err := s.db.QueryRowContext(ctx, `SELECT p.session_hash, s.expires_at FROM preview_sessions p JOIN sessions s ON s.token_hash = p.session_hash
		WHERE p.token_hash = ? AND p.project = ? AND p.preview = ? AND s.expires_at > ?`, HashToken(token), project, preview, now).Scan(&session, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	user, err := scanUser(s.db.QueryRowContext(ctx, userSelect+`JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`, session, now))
	return user, session, time.Unix(expires, 0), err
}

// PurgePreviewTickets drops the tickets that expired unspent. The preview sessions go with the hub's.
func (s *Store) PurgePreviewTickets(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM preview_tickets WHERE expires_at <= ?`, now.Unix())
	return err
}
