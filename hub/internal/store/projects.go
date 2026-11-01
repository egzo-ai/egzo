package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/egzo-ai/egzo/internal/hublink"
)

// Registration is a project as the hub knows it: a link that presented a key and a link code, which is how it came to exist. Its key,
// name, host, release and templates are what the link said; they are data, and a project speaks for itself only.
type Registration struct {
	ID        string
	Key       []byte
	Name      string
	Host      string
	Release   string
	Templates []string
	State     string // hublink.StateApproved or StateRevoked
	Owner     string // the user who made the link code it came with
	Message   string // why it was revoked
	Created   time.Time
	Decided   time.Time // when it was approved, then when it was revoked
	// Offline is when its link went away; zero while it is connected.
	Offline time.Time
}

// Online reports whether its link is connected.
func (r Registration) Online() bool { return r.Offline.IsZero() }

const registrationColumns = `id, key, name, host, release, templates, state, COALESCE(owner, ''), message, created_at, decided_at, offline_since`

func scanRegistration(row scanner) (Registration, error) {
	var r Registration
	var templates string
	var created int64
	var decided, offline sql.NullInt64
	if err := row.Scan(&r.ID, &r.Key, &r.Name, &r.Host, &r.Release, &templates, &r.State, &r.Owner, &r.Message, &created, &decided, &offline); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Registration{}, ErrNotFound
		}
		return Registration{}, err
	}
	r.Created = time.UnixMilli(created)
	if decided.Valid {
		r.Decided = time.UnixMilli(decided.Int64)
	}
	if offline.Valid {
		r.Offline = time.UnixMilli(offline.Int64)
	}
	return r, json.Unmarshal([]byte(templates), &r.Templates)
}

// Registration reads a project by its id.
func (s *Store) Registration(ctx context.Context, id string) (Registration, error) {
	return scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE id = ?`, id))
}

// RegistrationByKey finds the project of a link's key.
func (s *Store) RegistrationByKey(ctx context.Context, key []byte) (Registration, error) {
	return scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE key = ?`, key))
}

// Registrations lists every project, the newest first.
func (s *Store) Registrations(ctx context.Context) ([]Registration, error) {
	return s.registrations(ctx, `ORDER BY created_at DESC, id`)
}

// ApprovedProjects lists the projects the hub shows, by name.
func (s *Store) ApprovedProjects(ctx context.Context) ([]Registration, error) {
	return s.registrations(ctx, `WHERE state = ? ORDER BY name, host, id`, hublink.StateApproved)
}

func (s *Store) registrations(ctx context.Context, where string, args ...any) ([]Registration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+registrationColumns+` FROM projects `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Registration
	for rows.Next() {
		r, err := scanRegistration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConnectedProjects counts the projects a person connected that are on the hub: what their quota is measured against. Who connected a project
// is what counts, not who owns it now, so being invited as an owner spends nothing of anyone's quota.
func (s *Store) ConnectedProjects(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE owner = ? AND state = ?`, userID, hublink.StateApproved).Scan(&n)
	return n, err
}

// Connected records that a project's link is here, and what it says of itself now.
func (s *Store) Connected(ctx context.Context, id, name, host, release string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET name = ?, host = ?, release = ?, offline_since = NULL WHERE id = ?`, name, host, release, id)
	return err
}

// Disconnected records that a project's link went away.
func (s *Store) Disconnected(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET offline_since = ? WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// AllOffline marks every project as offline from at, unless it is already: no link is connected to a hub that has just started.
func (s *Store) AllOffline(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET offline_since = ? WHERE offline_since IS NULL`, at.UnixMilli())
	return err
}

// Revoke takes back a project that was approved. Its key stays refused until an operator forgets the project.
func (s *Store) Revoke(ctx context.Context, id, why string) (Registration, error) {
	r, err := s.Registration(ctx, id)
	if err != nil {
		return Registration{}, err
	}
	if r.State != hublink.StateApproved {
		return Registration{}, ErrNotFound
	}
	r.State, r.Message, r.Decided = hublink.StateRevoked, why, time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET state = ?, message = ?, decided_at = ? WHERE id = ?`, r.State, why, r.Decided.UnixMilli(), id); err != nil {
		return Registration{}, err
	}
	if err := dropFallenInvites(ctx, tx); err != nil {
		return Registration{}, err
	}
	return r, tx.Commit()
}

// ForgetProject removes a project that is in state, and everything keyed by it: its roles, its denials, its tasks. The audit log stays.
// The state is part of the one statement, so that an Unlink and a Revoke cannot pass each other: a project in another state is not
// found. A link that comes back with the same key needs a new code.
func (s *Store) ForgetProject(ctx context.Context, id, state string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND state = ?`, id, state)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
