// Package store is the hub's database: users, grants, engines, sessions and the audit log. SQLite, one file, no cgo.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

// Store is the hub database.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	identity TEXT NOT NULL UNIQUE,   -- issuer + "\n" + subject
	email TEXT NOT NULL,
	name TEXT NOT NULL,
	is_operator INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grants (
	engine TEXT NOT NULL,
	project TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	PRIMARY KEY (engine, project, user_id)
);
CREATE TABLE IF NOT EXISTS engines (
	name TEXT PRIMARY KEY,
	host TEXT NOT NULL,
	ssh_user TEXT NOT NULL DEFAULT '',
	host_key TEXT NOT NULL DEFAULT '',    -- the pinned ssh host key fingerprint
	credentials BLOB,                     -- sealed JSON: CA, cert, key, ssh key
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	at INTEGER NOT NULL,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	engine TEXT NOT NULL DEFAULT '',
	project TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	allowed INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_project ON audit (engine, project, id);
`

// Open opens (and creates) the database at path; ":memory:" gives a private one for tests.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	} else {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serialises writers anyway, and an in-memory database exists per connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare the database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// User is a person who has logged in.
type User struct {
	ID         string
	Identity   string
	Email      string
	Name       string
	IsOperator bool
	CreatedAt  time.Time
}

// Subject is the user as the permission rules see them.
func (u User) Subject() rbac.Subject { return rbac.Subject{ID: u.ID, IsOperator: u.IsOperator} }

// DisplayName is what the UI calls the user.
func (u User) DisplayName() string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return u.ID
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// LoginUser returns the user of an identity, creating them on the first login. The very first user is the operator.
// created tells whether this call made the user.
func (s *Store) LoginUser(ctx context.Context, identity, email, name string) (user User, created bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id, identity, email, name, is_operator, created_at FROM users WHERE identity = ?`, identity)
	user, err = scanUser(row)
	switch {
	case err == nil:
		if user.Email != email || user.Name != name {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET email = ?, name = ? WHERE id = ?`, email, name, user.ID); err != nil {
				return User{}, false, err
			}
			user.Email, user.Name = email, name
		}
		return user, false, tx.Commit()
	case !errors.Is(err, ErrNotFound):
		return User{}, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return User{}, false, err
	}
	user = User{ID: newID("u", 6), Identity: identity, Email: email, Name: name, IsOperator: count == 0, CreatedAt: time.Now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, identity, email, name, is_operator, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		user.ID, identity, email, name, boolInt(user.IsOperator), user.CreatedAt.Unix()); err != nil {
		return User{}, false, err
	}
	return user, true, tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scanner interface{ Scan(...any) error }

func scanUser(row scanner) (User, error) {
	var u User
	var operator int
	var created int64
	if err := row.Scan(&u.ID, &u.Identity, &u.Email, &u.Name, &operator, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.IsOperator, u.CreatedAt = operator == 1, time.Unix(created, 0)
	return u, nil
}

const userColumns = `id, identity, email, name, is_operator, created_at`

func (s *Store) User(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ErrLastOperator is returned when a change would leave the hub with no operator.
var ErrLastOperator = errors.New("the last operator cannot be removed")

// SetOperator makes a user an operator or takes it away. The last operator cannot be demoted, by themselves or by anyone: one
// transaction counts and writes. It reports whether anything changed, and returns ErrNotFound for a user that does not exist.
func (s *Store) SetOperator(ctx context.Context, id string, on bool) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT is_operator FROM users WHERE id = ?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if (current == 1) == on {
		return false, nil
	}
	if !on {
		var operators int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_operator = 1`).Scan(&operators); err != nil {
			return false, err
		}
		if operators <= 1 {
			return false, ErrLastOperator
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET is_operator = ? WHERE id = ?`, boolInt(on), id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DeleteUser removes a user and, by cascade, their grants and sessions. An operator cannot be deleted: demote them first.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND is_operator = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- grants ------------------------------------------------------------------------------------------------------------------

// Grant is a role held by a user on one project of one engine.
type Grant struct {
	Engine, Project, UserID string
	Role                    rbac.Role
}

// SetRole grants role, replaces the role, or (with rbac.None) removes the grant.
func (s *Store) SetRole(ctx context.Context, engine, project, userID string, role rbac.Role) error {
	if role == rbac.None {
		_, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE engine = ? AND project = ? AND user_id = ?`, engine, project, userID)
		return err
	}
	if !role.Valid() {
		return fmt.Errorf("invalid role %q", role)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO grants (engine, project, user_id, role) VALUES (?, ?, ?, ?)
		ON CONFLICT (engine, project, user_id) DO UPDATE SET role = excluded.role`, engine, project, userID, string(role))
	return err
}

// Role is what a user holds on a project, rbac.None when nothing.
func (s *Store) Role(ctx context.Context, engine, project, userID string) (rbac.Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM grants WHERE engine = ? AND project = ? AND user_id = ?`, engine, project, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return rbac.None, nil
	}
	return rbac.Role(role), err
}

// GrantsOf lists every grant of a user.
func (s *Store) GrantsOf(ctx context.Context, userID string) ([]Grant, error) {
	return s.grants(ctx, `SELECT engine, project, user_id, role FROM grants WHERE user_id = ? ORDER BY engine, project`, userID)
}

// GrantsOn lists every grant on a project.
func (s *Store) GrantsOn(ctx context.Context, engine, project string) ([]Grant, error) {
	return s.grants(ctx, `SELECT engine, project, user_id, role FROM grants WHERE engine = ? AND project = ? ORDER BY user_id`, engine, project)
}

func (s *Store) grants(ctx context.Context, query string, args ...any) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []Grant
	for rows.Next() {
		var g Grant
		var role string
		if err := rows.Scan(&g.Engine, &g.Project, &g.UserID, &role); err != nil {
			return nil, err
		}
		g.Role = rbac.Role(role)
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// --- sessions ----------------------------------------------------------------------------------------------------------------

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSession makes a login session and returns its token; only the token's hash is stored.
func (s *Store) NewSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	token := newID("", 32)
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, time.Now().Add(ttl).Unix())
	return token, err
}

// SessionUser returns the user a live session token belongs to.
func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNotFound
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumnsOf("u")+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), time.Now().Unix()))
}

func userColumnsOf(alias string) string {
	return alias + ".id, " + alias + ".identity, " + alias + ".email, " + alias + ".name, " + alias + ".is_operator, " + alias + ".created_at"
}

func (s *Store) EndSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// PurgeSessions drops the expired ones.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// --- engines -----------------------------------------------------------------------------------------------------------------

// EngineRow is a stored engine; Credentials is still sealed.
type EngineRow struct {
	Name, Host, SSHUser, HostKey string
	Credentials                  []byte
	CreatedAt                    time.Time
}

func (s *Store) PutEngine(ctx context.Context, e EngineRow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO engines (name, host, ssh_user, host_key, credentials, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET host = excluded.host, ssh_user = excluded.ssh_user, host_key = excluded.host_key, credentials = excluded.credentials`,
		e.Name, e.Host, e.SSHUser, e.HostKey, e.Credentials, time.Now().Unix())
	return err
}

func (s *Store) Engine(ctx context.Context, name string) (EngineRow, error) {
	var e EngineRow
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT name, host, ssh_user, host_key, credentials, created_at FROM engines WHERE name = ?`, name).
		Scan(&e.Name, &e.Host, &e.SSHUser, &e.HostKey, &e.Credentials, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.CreatedAt = time.Unix(created, 0)
	return e, err
}

func (s *Store) Engines(ctx context.Context) ([]EngineRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, host, ssh_user, host_key, credentials, created_at FROM engines ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var engines []EngineRow
	for rows.Next() {
		var e EngineRow
		var created int64
		if err := rows.Scan(&e.Name, &e.Host, &e.SSHUser, &e.HostKey, &e.Credentials, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0)
		engines = append(engines, e)
	}
	return engines, rows.Err()
}

// DeleteEngine removes an engine and the grants that were on its projects.
func (s *Store) DeleteEngine(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM engines WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM grants WHERE engine = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// --- audit -------------------------------------------------------------------------------------------------------------------

// AuditEntry is one thing somebody did, or was refused.
type AuditEntry struct {
	ID                     int64
	At                     time.Time
	Actor, Action          string
	Engine, Project, Agent string
	Detail                 string
	Allowed                bool
}

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit (at, actor, action, engine, project, agent, detail, allowed) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Actor, e.Action, e.Engine, e.Project, e.Agent, e.Detail, boolInt(e.Allowed))
	return err
}

// AuditOf lists the latest entries about a project, newest first.
func (s *Store) AuditOf(ctx context.Context, engine, project string, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, actor, action, engine, project, agent, detail, allowed FROM audit
		WHERE engine = ? AND project = ? ORDER BY id DESC LIMIT ?`, engine, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var allowed int
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Engine, &e.Project, &e.Agent, &e.Detail, &allowed); err != nil {
			return nil, err
		}
		e.At, e.Allowed = time.Unix(at, 0), allowed == 1
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
