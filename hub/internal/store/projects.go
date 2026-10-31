package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// Registration is a project as the hub knows it: a link that presented a key, and what a person decided about it. Its key, name, host,
// release and templates are what the link said; they are data, and a project speaks for itself only.
type Registration struct {
	ID        string
	Key       []byte
	Name      string
	Host      string
	Release   string
	Templates []string
	State     string // hublink.StatePending, StateApproved, StateRefused or StateRevoked
	Code      string // the device code, while it is pending
	Owner     string // the user who approved it
	Message   string // why it was refused or revoked
	Created   time.Time
	Decided   time.Time // zero while it is pending
	// Offline is when its link went away; zero while it is connected.
	Offline time.Time
}

// Online reports whether its link is connected.
func (r Registration) Online() bool { return r.Offline.IsZero() }

// MaxPending is how many projects may wait for a person at once. Registering takes nothing but a key, from whoever reaches the link
// endpoint, so what it can make the hub keep has a limit.
const MaxPending = 200

// ErrTooManyPending is returned when MaxPending projects wait, each with its link connected.
var ErrTooManyPending = errors.New("too many projects are waiting for a person")

const registrationColumns = `id, key, name, host, release, templates, state, COALESCE(code, ''), COALESCE(owner, ''), message, created_at, decided_at, offline_since`

func scanRegistration(row scanner) (Registration, error) {
	var r Registration
	var templates string
	var created int64
	var decided, offline sql.NullInt64
	if err := row.Scan(&r.ID, &r.Key, &r.Name, &r.Host, &r.Release, &templates, &r.State, &r.Code, &r.Owner, &r.Message, &created, &decided, &offline); err != nil {
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

// Register keeps a link that presented a new key as a pending project, with a device code of its own. The oldest project that waits
// with nobody connected makes room when MaxPending wait.
func (s *Store) Register(ctx context.Context, r Registration) (Registration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, err
	}
	defer tx.Rollback()
	var waiting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE state = ?`, hublink.StatePending).Scan(&waiting); err != nil {
		return Registration{}, err
	}
	if waiting >= MaxPending {
		result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = (SELECT id FROM projects WHERE state = ? AND offline_since IS NOT NULL
			ORDER BY offline_since LIMIT 1)`, hublink.StatePending)
		if err != nil {
			return Registration{}, err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return Registration{}, ErrTooManyPending
		}
	}
	templates, err := json.Marshal(append([]string{}, r.Templates...))
	if err != nil {
		return Registration{}, err
	}
	r.ID, r.State, r.Created = newID("p", 6), hublink.StatePending, time.Now()
	for {
		r.Code = newCode()
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE code = ?`, r.Code).Scan(&taken); err != nil {
			return Registration{}, err
		}
		if taken == 0 {
			break
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, key, name, host, release, templates, state, code, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Key, r.Name, r.Host, r.Release, string(templates), r.State, r.Code, r.Created.UnixMilli()); err != nil {
		return Registration{}, err
	}
	return r, tx.Commit()
}

// codeLetters are those a person does not mistake for each other: no I, L, O, 0 or 1.
const codeLetters = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newCode is a device code like QXRB-7KPM. It chooses nothing by itself: it only names what a signed-in operator looks at.
func newCode() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	code := make([]byte, 0, 9)
	for i, b := range raw {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, codeLetters[int(b)%len(codeLetters)])
	}
	return string(code)
}

// Registration reads a project by its id.
func (s *Store) Registration(ctx context.Context, id string) (Registration, error) {
	return scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE id = ?`, id))
}

// RegistrationByKey finds the project of a link's key.
func (s *Store) RegistrationByKey(ctx context.Context, key []byte) (Registration, error) {
	return scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE key = ?`, key))
}

// RegistrationByCode finds the project that waits under a device code, whatever the case it was typed in.
func (s *Store) RegistrationByCode(ctx context.Context, code string) (Registration, error) {
	return scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE code = ?`, strings.ToUpper(strings.TrimSpace(code))))
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

// Approve decides for a project that waits under code: it is linked, and whoever approved it owns it, with a grant like any other, which
// stays when they stop being an operator.
func (s *Store) Approve(ctx context.Context, code, userID string) (Registration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, err
	}
	defer tx.Rollback()
	r, err := scanRegistration(tx.QueryRowContext(ctx, `SELECT `+registrationColumns+` FROM projects WHERE code = ?`, strings.ToUpper(strings.TrimSpace(code))))
	if err != nil {
		return Registration{}, err
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET state = ?, code = NULL, owner = ?, decided_at = ? WHERE id = ?`, hublink.StateApproved, userID, now.UnixMilli(), r.ID); err != nil {
		return Registration{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants (project, user_id, role) VALUES (?, ?, ?)`, r.ID, userID, string(rbac.Owner)); err != nil {
		return Registration{}, err
	}
	r.State, r.Code, r.Owner, r.Decided = hublink.StateApproved, "", userID, now
	return r, tx.Commit()
}

// Refuse decides against a project that waits under code. Its key stays refused until an operator forgets the project.
func (s *Store) Refuse(ctx context.Context, code, why string) (Registration, error) {
	r, err := s.RegistrationByCode(ctx, code)
	if err != nil {
		return Registration{}, err
	}
	return r, s.closeRegistration(ctx, &r, hublink.StateRefused, why)
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
	return r, s.closeRegistration(ctx, &r, hublink.StateRevoked, why)
}

func (s *Store) closeRegistration(ctx context.Context, r *Registration, state, why string) error {
	now := time.Now()
	r.State, r.Code, r.Message, r.Decided = state, "", why, now
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET state = ?, code = NULL, message = ?, decided_at = ? WHERE id = ?`, state, why, now.UnixMilli(), r.ID)
	return err
}

// ForgetProject removes a project, and everything keyed by it: its roles, its denials, its tasks. The audit log stays. A link that comes
// back with the same key is a new registration.
func (s *Store) ForgetProject(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgePending drops the projects that wait with nobody connected since before: a registration nobody came to.
func (s *Store) PurgePending(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE state = ? AND offline_since < ?`, hublink.StatePending, before.UnixMilli())
	return err
}
