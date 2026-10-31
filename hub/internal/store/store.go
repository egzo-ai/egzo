// Package store is the hub's database: users, grants, the projects that linked, sessions, the audit log, the tasks and what the proxies
// denied. SQLite, one file, no cgo.
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

// schemaVersion is kept in SQLite's user_version. Pre-1.0 has no migrations: a database of another version is refused.
const schemaVersion = 6

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE, -- disabled users keep theirs: a name is never freed
	is_operator INTEGER NOT NULL DEFAULT 0,
	disabled_at INTEGER,                          -- null: enabled
	project_quota INTEGER NOT NULL DEFAULT 0,     -- how many projects they may connect; an operator is not held to it
	created_at INTEGER NOT NULL,
	last_login_at INTEGER
);
CREATE TABLE IF NOT EXISTS user_passwords (
	user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	hash TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_identities (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	issuer TEXT NOT NULL,
	subject TEXT NOT NULL,
	email TEXT NOT NULL DEFAULT '',
	email_verified INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL DEFAULT '',
	preferred_username TEXT NOT NULL DEFAULT '',
	picture_url TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	last_login_at INTEGER NOT NULL,
	UNIQUE (issuer, subject),
	UNIQUE (user_id, issuer)
);
-- A project is a registration: a link that presented a key and a link code (projects.go). Its id is what everything else is keyed by.
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	key BLOB NOT NULL UNIQUE,             -- the link's public key, which is how the hub knows it
	name TEXT NOT NULL,                   -- as the link says: two projects may have the same one
	host TEXT NOT NULL DEFAULT '',        -- the machine it says it runs on
	release TEXT NOT NULL DEFAULT '',     -- the egzo it says it runs
	templates TEXT NOT NULL DEFAULT '[]', -- JSON, what it said when it registered
	state TEXT NOT NULL,                  -- approved or revoked
	owner TEXT REFERENCES users(id) ON DELETE SET NULL, -- who made the code it came with
	message TEXT NOT NULL DEFAULT '',     -- why it was revoked
	created_at INTEGER NOT NULL,
	decided_at INTEGER,
	offline_since INTEGER                 -- Unix milliseconds; null while its link is connected
);
CREATE INDEX IF NOT EXISTS projects_state ON projects (state, created_at);
-- A link code is what lets a project register: a person with quota makes one, and the project presents it, once (linkcodes.go).
CREATE TABLE IF NOT EXISTS link_codes (
	id TEXT PRIMARY KEY,
	hash BLOB NOT NULL UNIQUE,            -- SHA-256 of the code: the code itself is shown once and kept nowhere
	created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grants (
	project TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	PRIMARY KEY (project, user_id)
);
-- An invite is what lets a person in: one code, good once, that grants a role on a project or the flags on a user (invites.go).
CREATE TABLE IF NOT EXISTS invites (
	id TEXT PRIMARY KEY,
	hash BLOB NOT NULL UNIQUE,            -- SHA-256 of the code: the code itself is shown once and kept nowhere
	created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	project TEXT REFERENCES projects(id) ON DELETE CASCADE, -- null: it grants the flags below
	role TEXT NOT NULL DEFAULT '',
	operator INTEGER NOT NULL DEFAULT 0,
	project_quota INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	method TEXT NOT NULL CHECK (method IN ('password', 'identity')),
	identity_id TEXT REFERENCES user_identities(id) ON DELETE CASCADE, -- set for an identity
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	at INTEGER NOT NULL,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	project TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	allowed INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_project ON audit (project, id);
-- What the proxies denied, grouped: one row per instance, protocol, host and reason code (roadmap/proxy-audit.md).
CREATE TABLE IF NOT EXISTS denials (
	project TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	instance TEXT NOT NULL,              -- the instance's id; '' when the proxy could not tell who it was
	protocol TEXT NOT NULL,
	host TEXT NOT NULL,
	code TEXT NOT NULL,
	event TEXT NOT NULL,                 -- the step that was refused or failed
	decision TEXT NOT NULL,              -- deny or error
	agent TEXT NOT NULL DEFAULT '',      -- the instance's name when it was last seen
	claimed_agent TEXT NOT NULL DEFAULT '', -- the name the client sent in its proxy credentials, proved or not
	client TEXT NOT NULL DEFAULT '',     -- the address of the latest one
	user_agent TEXT NOT NULL DEFAULT '', -- what the latest client said it was
	actor TEXT NOT NULL DEFAULT '',      -- who spawned the instance, when the hub knew it
	profile TEXT NOT NULL DEFAULT '',
	server TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',       -- the latest reason, for people
	count INTEGER NOT NULL,
	first_at INTEGER NOT NULL,           -- Unix milliseconds
	last_at INTEGER NOT NULL,
	fixed_at INTEGER,                    -- the same instance got through since the last one
	PRIMARY KEY (project, instance, claimed_agent, protocol, host, code)
);
CREATE INDEX IF NOT EXISTS denials_recent ON denials (project, last_at);
-- What the hub did on the projects, and how it went (hub/internal/hub/tasks.go). Times are Unix milliseconds.
CREATE TABLE IF NOT EXISTS tasks (
	id TEXT PRIMARY KEY,
	verb TEXT NOT NULL,                  -- spawn, start, stop or rm
	project TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	agent TEXT NOT NULL,
	template TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL,
	state TEXT NOT NULL,                 -- running, done, failed or interrupted
	step TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL,
	ended_at INTEGER,
	dismissed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tasks_project ON tasks (project, started_at);
CREATE INDEX IF NOT EXISTS tasks_open ON tasks (state, dismissed);
-- How far the hub read each project's audit trail: the time of the last event it handled.
CREATE TABLE IF NOT EXISTS audit_cursors (
	project TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	at INTEGER NOT NULL                  -- Unix nanoseconds
);
`

// ErrOldSchema is returned by Open for a database made by another version of the hub.
var ErrOldSchema = errors.New("the database was made by another version of the hub")

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
	if err := prepare(db); err != nil {
		db.Close()
		if errors.Is(err, ErrOldSchema) {
			return nil, fmt.Errorf("%w: remove %s and start again (before 1.0 the hub has no migrations)", err, path)
		}
		return nil, fmt.Errorf("prepare the database: %w", err)
	}
	return &Store{db: db}, nil
}

// prepare makes the tables of a new database, and refuses one of another version.
func prepare(db *sql.DB) error {
	var version, tables int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 && version != schemaVersion {
		return ErrOldSchema
	}
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func newID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// StarterUsername is the username of the starter account, the one that signs in with a password.
const StarterUsername = "admin"

// User is a person as the hub knows them. Name, Email, EmailVerified and Picture come from the identity they signed in with most
// recently, for display only; HasPassword and Issuers say how they can sign in.
type User struct {
	ID          string
	Username    string
	IsOperator  bool
	DisabledAt  time.Time // zero: enabled
	CreatedAt   time.Time
	LastLoginAt time.Time // zero: never
	// ProjectQuota is how many projects they may connect to the hub. Lowering it under what they have removes nothing.
	ProjectQuota int

	Name, Email   string
	EmailVerified bool
	Picture       string
	HasPassword   bool
}

// Disabled reports whether the user may not sign in.
func (u User) Disabled() bool { return !u.DisabledAt.IsZero() }

// Subject is the user as the permission rules see them.
func (u User) Subject() rbac.Subject { return rbac.Subject{ID: u.ID, IsOperator: u.IsOperator} }

// DisplayName is what the UI calls the user: their username, which the hub chose and which no provider changes.
func (u User) DisplayName() string { return u.Username }

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scanner interface{ Scan(...any) error }

// userSelect reads a user with the display fields of their latest identity; append a WHERE on u.
const userSelect = `SELECT u.id, u.username, u.is_operator, u.disabled_at, u.created_at, u.last_login_at, u.project_quota,
	COALESCE(i.name, ''), COALESCE(i.email, ''), COALESCE(i.email_verified, 0), COALESCE(i.picture_url, ''),
	EXISTS (SELECT 1 FROM user_passwords p WHERE p.user_id = u.id)
	FROM users u LEFT JOIN user_identities i ON i.id =
		(SELECT id FROM user_identities WHERE user_id = u.id ORDER BY last_login_at DESC, created_at DESC LIMIT 1) `

func scanUser(row scanner) (User, error) {
	var u User
	var operator, verified, password int
	var created int64
	var disabled, login sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &operator, &disabled, &created, &login, &u.ProjectQuota, &u.Name, &u.Email, &verified, &u.Picture, &password); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.IsOperator, u.CreatedAt, u.EmailVerified, u.HasPassword = operator == 1, time.Unix(created, 0), verified == 1, password == 1
	if disabled.Valid {
		u.DisabledAt = time.Unix(disabled.Int64, 0)
	}
	if login.Valid {
		u.LastLoginAt = time.Unix(login.Int64, 0)
	}
	return u, nil
}

func (s *Store) User(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.id = ?`, id))
}

// UserByName finds a user by username, whatever the case.
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.username = ?`, username))
}

// Users lists everyone, disabled users included, in the order they were made.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+`ORDER BY u.created_at, u.rowid`)
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

// insertUser makes a user with a free username built from base: base itself, else base-2, base-3, ... The unique index settles
// two logins at once.
func insertUser(ctx context.Context, tx *sql.Tx, base string, operator bool) (User, error) {
	now := time.Now()
	for n := 1; ; n++ {
		name := withSuffix(base, n)
		if n == 1 && reserved[name] {
			continue
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, name).Scan(&taken); err != nil {
			return User{}, err
		}
		if taken > 0 {
			continue
		}
		return insertNamed(ctx, tx, name, operator, now)
	}
}

func insertNamed(ctx context.Context, tx *sql.Tx, name string, operator bool, now time.Time) (User, error) {
	u := User{ID: newID("u", 6), Username: name, IsOperator: operator, CreatedAt: now, LastLoginAt: now}
	_, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, is_operator, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Username, boolInt(operator), now.Unix(), now.Unix())
	return u, err
}

// SetProjectQuota changes how many projects a user may connect. It reports whether anything changed.
func (s *Store) SetProjectQuota(ctx context.Context, id string, quota int) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET project_quota = ? WHERE id = ? AND project_quota != ?`, quota, id, quota)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// EnsureStarter makes sure the starter account exists, is enabled and is an operator, making or repairing it as needed.
func (s *Store) EnsureStarter(ctx context.Context) (User, error) {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id, username, is_operator, created_at) VALUES (?, ?, 1, ?)
		ON CONFLICT (username) DO UPDATE SET is_operator = 1, disabled_at = NULL`, newID("u", 6), StarterUsername, now); err != nil {
		return User{}, err
	}
	return s.UserByName(ctx, StarterUsername)
}

// ErrLastOperator is returned when a change would leave the hub with no operator.
var ErrLastOperator = errors.New("the last operator cannot be removed")

// SetOperator makes a user an operator or takes it away. The last operator cannot be demoted, by themselves or by anyone: one
// transaction counts and writes. It reports whether anything changed, and returns ErrNotFound for a user that does not exist.
// A disabled user cannot be made an operator (ErrDisabled).
func (s *Store) SetOperator(ctx context.Context, id string, on bool) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current int
	var disabled sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT is_operator, disabled_at FROM users WHERE id = ?`, id).Scan(&current, &disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if (current == 1) == on {
		return false, nil
	}
	if on && disabled.Valid {
		return false, ErrDisabled
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

// CountOperators counts the operators. Operators cannot be disabled, so every one of them is enabled.
func (s *Store) CountOperators(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_operator = 1 AND disabled_at IS NULL`).Scan(&n)
	return n, err
}

// OperatorsWithIdentityAt counts the enabled operators who can sign in at issuer.
func (s *Store) OperatorsWithIdentityAt(ctx context.Context, issuer string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT u.id) FROM users u JOIN user_identities i ON i.user_id = u.id
		WHERE u.is_operator = 1 AND u.disabled_at IS NULL AND i.issuer = ?`, issuer).Scan(&n)
	return n, err
}

// ErrDisabled is returned for a user who is disabled, where that refuses the change.
var ErrDisabled = errors.New("the user is disabled")

// ErrOperator is returned when an operator would be disabled: demote them first.
var ErrOperator = errors.New("an operator cannot be disabled")

// SetDisabled disables or enables a user. Disabling ends every session of theirs, and returns the ended sessions' hashes so their
// terminals can be closed. An operator cannot be disabled. It reports whether anything changed.
func (s *Store) SetDisabled(ctx context.Context, id string, on bool) (changed bool, ended []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	var operator int
	var disabled sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT is_operator, disabled_at FROM users WHERE id = ?`, id).Scan(&operator, &disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, ErrNotFound
		}
		return false, nil, err
	}
	if disabled.Valid == on {
		return false, nil, nil
	}
	if on {
		if operator == 1 {
			return false, nil, ErrOperator
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE id = ?`, time.Now().Unix(), id); err != nil {
			return false, nil, err
		}
		if ended, err = endSessions(ctx, tx, `user_id = ?`, id); err != nil {
			return false, nil, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = NULL WHERE id = ?`, id); err != nil {
		return false, nil, err
	}
	return true, ended, tx.Commit()
}

// --- passwords ---------------------------------------------------------------------------------------------------------------

// PasswordHash returns a user's password hash, ErrNotFound when they have none.
func (s *Store) PasswordHash(ctx context.Context, userID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM user_passwords WHERE user_id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// SetPassword saves a user's password hash and ends every password session of theirs: whoever signed in with the old one is out.
func (s *Store) SetPassword(ctx context.Context, userID, hash string) (ended []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_passwords (user_id, hash, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET hash = excluded.hash, updated_at = excluded.updated_at`, userID, hash, time.Now().Unix()); err != nil {
		return nil, err
	}
	if ended, err = endSessions(ctx, tx, `user_id = ? AND method = 'password'`, userID); err != nil {
		return nil, err
	}
	return ended, tx.Commit()
}

// MarkLogin records that a user signed in now.
func (s *Store) MarkLogin(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().Unix(), userID)
	return err
}

// --- identities --------------------------------------------------------------------------------------------------------------

// Profile is what a provider says about an account: a snapshot, refreshed at every sign-in, for display only.
type Profile struct {
	Email             string
	EmailVerified     bool
	Name              string
	PreferredUsername string
	Picture           string
}

// Identity is one account at one provider, found by (Issuer, Subject).
type Identity struct {
	ID, UserID      string
	Issuer, Subject string
	Profile
	CreatedAt, LastLoginAt time.Time
}

const identityColumns = `id, user_id, issuer, subject, email, email_verified, name, preferred_username, picture_url, created_at, last_login_at`

func scanIdentity(row scanner) (Identity, error) {
	var i Identity
	var verified int
	var created, login int64
	if err := row.Scan(&i.ID, &i.UserID, &i.Issuer, &i.Subject, &i.Email, &verified, &i.Name, &i.PreferredUsername, &i.Picture, &created, &login); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	i.EmailVerified, i.CreatedAt, i.LastLoginAt = verified == 1, time.Unix(created, 0), time.Unix(login, 0)
	return i, nil
}

// Identities lists the identities of a user, or of everyone when userID is empty.
func (s *Store) Identities(ctx context.Context, userID string) ([]Identity, error) {
	query, args := `SELECT `+identityColumns+` FROM user_identities ORDER BY created_at, rowid`, []any{}
	if userID != "" {
		query, args = `SELECT `+identityColumns+` FROM user_identities WHERE user_id = ? ORDER BY created_at, rowid`, []any{userID}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Identity
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func refreshIdentity(ctx context.Context, tx *sql.Tx, id string, p Profile, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE user_identities SET email = ?, email_verified = ?, name = ?, preferred_username = ?, picture_url = ?,
		last_login_at = ? WHERE id = ?`, p.Email, boolInt(p.EmailVerified), p.Name, p.PreferredUsername, p.Picture, now, id)
	return err
}

func insertIdentity(ctx context.Context, tx *sql.Tx, userID, issuer, subject string, p Profile, now int64) (string, error) {
	id := newID("i", 6)
	_, err := tx.ExecContext(ctx, `INSERT INTO user_identities (id, user_id, issuer, subject, email, email_verified, name, preferred_username,
		picture_url, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, issuer, subject, p.Email, boolInt(p.EmailVerified), p.Name, p.PreferredUsername, p.Picture, now, now)
	return id, err
}

// LoginIdentity signs in with an identity. Found and enabled: its snapshot and the times are refreshed, and the invite is left alone.
// Found and disabled: ErrDisabled, and nothing is written. Not found: it takes an invite, which is spent on a new user who owns the identity
// (created is set); without one ErrNoInvite, with a bad one ErrBadInvite, and nothing is written. identityID is the identity signed in with,
// for the session. Two first logins at once cannot both make a user: the store has one connection, which the transaction holds from the
// lookup to the insert.
func (s *Store) LoginIdentity(ctx context.Context, issuer, subject string, p Profile, invite string) (User, string, Invite, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", Invite{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var identityID, userID string
	var disabled sql.NullInt64
	var spent Invite
	created := false
	err = tx.QueryRowContext(ctx, `SELECT i.id, u.id, u.disabled_at FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE i.issuer = ? AND i.subject = ?`, issuer, subject).Scan(&identityID, &userID, &disabled)
	switch {
	case err == nil:
		if disabled.Valid {
			return User{}, "", Invite{}, false, ErrDisabled
		}
		if err := refreshIdentity(ctx, tx, identityID, p, now); err != nil {
			return User{}, "", Invite{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID); err != nil {
			return User{}, "", Invite{}, false, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if invite == "" {
			return User{}, "", Invite{}, false, ErrNoInvite
		}
		u, err := insertUser(ctx, tx, UsernameFrom(p.PreferredUsername, p.Email, p.Name), false)
		if err != nil {
			return User{}, "", Invite{}, false, err
		}
		userID, created = u.ID, true
		if identityID, err = insertIdentity(ctx, tx, userID, issuer, subject, p, now); err != nil {
			return User{}, "", Invite{}, false, err
		}
		if spent, err = redeem(ctx, tx, invite, userID, time.Unix(now, 0)); err != nil {
			return User{}, "", Invite{}, false, err
		}
	default:
		return User{}, "", Invite{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", Invite{}, false, err
	}
	user, err := s.User(ctx, userID)
	return user, identityID, spent, created, err
}

// IdentityTakenError is returned when an identity to link belongs to somebody else.
type IdentityTakenError struct{ Username string }

func (e *IdentityTakenError) Error() string {
	return fmt.Sprintf("that account already belongs to %s", e.Username)
}

// ErrIssuerLinked is returned when a user already has an identity at the issuer of the one to link: one per issuer.
var ErrIssuerLinked = errors.New("an account at this provider is already linked: unlink it first")

// LinkIdentity adds an identity to a user who proved control of it. It refuses an identity of another user (identities never
// move), and a second identity at the same issuer. Linking the identity the user already has changes nothing.
func (s *Store) LinkIdentity(ctx context.Context, userID, issuer, subject string, p Profile) (Identity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	existing, err := scanIdentity(tx.QueryRowContext(ctx, `SELECT `+identityColumns+` FROM user_identities WHERE issuer = ? AND subject = ?`, issuer, subject))
	switch {
	case err == nil && existing.UserID == userID:
		if err := refreshIdentity(ctx, tx, existing.ID, p, now); err != nil {
			return Identity{}, err
		}
		return existing, tx.Commit()
	case err == nil:
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, existing.UserID).Scan(&owner); err != nil {
			return Identity{}, err
		}
		return Identity{}, &IdentityTakenError{Username: owner}
	case !errors.Is(err, ErrNotFound):
		return Identity{}, err
	}
	var linked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_identities WHERE user_id = ? AND issuer = ?`, userID, issuer).Scan(&linked); err != nil {
		return Identity{}, err
	}
	if linked > 0 {
		return Identity{}, ErrIssuerLinked
	}
	id, err := insertIdentity(ctx, tx, userID, issuer, subject, p, now)
	if err != nil {
		return Identity{}, err
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, err
	}
	return Identity{ID: id, UserID: userID, Issuer: issuer, Subject: subject, Profile: p, CreatedAt: time.Unix(now, 0), LastLoginAt: time.Unix(now, 0)}, nil
}

// ErrLastWayIn is returned when unlinking would leave a user no way to sign in.
var ErrLastWayIn = errors.New("this is your last way to sign in")

// UnlinkIdentity removes an identity of a user, and the sessions made with it. A way to sign in is a password (while
// passwordCounts, that is while local login is on) or an identity at currentIssuer: removing the last one is refused. Removing an
// identity of another issuer never is.
func (s *Store) UnlinkIdentity(ctx context.Context, userID, identityID, currentIssuer string, passwordCounts bool) (Identity, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, nil, err
	}
	defer tx.Rollback()
	identity, err := scanIdentity(tx.QueryRowContext(ctx, `SELECT `+identityColumns+` FROM user_identities WHERE id = ? AND user_id = ?`, identityID, userID))
	if err != nil {
		return Identity{}, nil, err
	}
	if currentIssuer != "" && identity.Issuer == currentIssuer {
		var others, password int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_identities WHERE user_id = ? AND issuer = ? AND id != ?`,
			userID, currentIssuer, identityID).Scan(&others); err != nil {
			return Identity{}, nil, err
		}
		if passwordCounts {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_passwords WHERE user_id = ?`, userID).Scan(&password); err != nil {
				return Identity{}, nil, err
			}
		}
		if others+password == 0 {
			return Identity{}, nil, ErrLastWayIn
		}
	}
	ended, err := removeIdentity(ctx, tx, identityID)
	if err != nil {
		return Identity{}, nil, err
	}
	return identity, ended, tx.Commit()
}

// RemoveIdentity takes an identity from a disabled user, so it can be linked to somebody else: the way out of an accidental
// second user. An enabled user's identity is theirs to unlink: that is refused with ErrNotDisabled.
func (s *Store) RemoveIdentity(ctx context.Context, userID, identityID string) (Identity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	var disabled sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT disabled_at FROM users WHERE id = ?`, userID).Scan(&disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	if !disabled.Valid {
		return Identity{}, ErrNotDisabled
	}
	identity, err := scanIdentity(tx.QueryRowContext(ctx, `SELECT `+identityColumns+` FROM user_identities WHERE id = ? AND user_id = ?`, identityID, userID))
	if err != nil {
		return Identity{}, err
	}
	if _, err := removeIdentity(ctx, tx, identityID); err != nil {
		return Identity{}, err
	}
	return identity, tx.Commit()
}

// ErrNotDisabled is returned when an operator removes an identity from a user who is not disabled.
var ErrNotDisabled = errors.New("only a disabled user's identity can be removed")

func removeIdentity(ctx context.Context, tx *sql.Tx, identityID string) ([]string, error) {
	ended, err := endSessions(ctx, tx, `identity_id = ?`, identityID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM user_identities WHERE id = ?`, identityID)
	return ended, err
}

// --- grants ------------------------------------------------------------------------------------------------------------------

// Grant is a role held by a user on one project.
type Grant struct {
	Project, UserID string
	Role            rbac.Role
}

// ErrLastOwner is returned when a change would leave a project without a valid owner.
var ErrLastOwner = errors.New("a project keeps at least one owner")

// SetRole grants role, replaces the role, or (with rbac.None) removes the grant. One transaction counts and writes: the last owner who can
// sign in stays one, whoever asks. A disabled owner does not count, so that one can be removed once there is a new owner.
func (s *Store) SetRole(ctx context.Context, project, userID string, role rbac.Role) error {
	if role != rbac.None && !role.Valid() {
		return fmt.Errorf("invalid role %q", role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if role != rbac.Owner {
		var valid, others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE g.user_id = ?), COUNT(*) FILTER (WHERE g.user_id != ?) FROM grants g JOIN users u ON u.id = g.user_id
			WHERE g.project = ? AND g.role = ? AND u.disabled_at IS NULL`, userID, userID, project, string(rbac.Owner)).Scan(&valid, &others); err != nil {
			return err
		}
		if valid > 0 && others == 0 {
			return ErrLastOwner
		}
	}
	if err := setGrant(ctx, tx, project, userID, role); err != nil {
		return err
	}
	return tx.Commit()
}

func setGrant(ctx context.Context, tx *sql.Tx, project, userID string, role rbac.Role) error {
	if role == rbac.None {
		_, err := tx.ExecContext(ctx, `DELETE FROM grants WHERE project = ? AND user_id = ?`, project, userID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO grants (project, user_id, role) VALUES (?, ?, ?)
		ON CONFLICT (project, user_id) DO UPDATE SET role = excluded.role`, project, userID, string(role))
	return err
}

// Role is what a user holds on a project, rbac.None when nothing.
func (s *Store) Role(ctx context.Context, project, userID string) (rbac.Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM grants WHERE project = ? AND user_id = ?`, project, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return rbac.None, nil
	}
	return rbac.Role(role), err
}

// GrantsOf lists every grant of a user.
func (s *Store) GrantsOf(ctx context.Context, userID string) ([]Grant, error) {
	return s.grants(ctx, `SELECT project, user_id, role FROM grants WHERE user_id = ? ORDER BY project`, userID)
}

// GrantsOn lists every grant on a project.
func (s *Store) GrantsOn(ctx context.Context, project string) ([]Grant, error) {
	return s.grants(ctx, `SELECT project, user_id, role FROM grants WHERE project = ? ORDER BY user_id`, project)
}

// ValidOwners lists the usernames of the enabled owners of each project: one that is not in the answer has nobody who can answer for it.
func (s *Store) ValidOwners(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.project, u.username FROM grants g JOIN users u ON u.id = g.user_id
		WHERE g.role = ? AND u.disabled_at IS NULL ORDER BY g.project, u.username`, string(rbac.Owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string][]string{}
	for rows.Next() {
		var project, username string
		if err := rows.Scan(&project, &username); err != nil {
			return nil, err
		}
		owners[project] = append(owners[project], username)
	}
	return owners, rows.Err()
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
		if err := rows.Scan(&g.Project, &g.UserID, &role); err != nil {
			return nil, err
		}
		g.Role = rbac.Role(role)
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// --- sessions ----------------------------------------------------------------------------------------------------------------

// HashToken is how a session token is kept: only its hash is stored, and it names the session elsewhere (its terminals).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Session methods: how a session was made.
const (
	MethodPassword = "password"
	MethodIdentity = "identity"
)

// NewSession makes a login session and returns its token; only the token's hash is stored. identityID is the identity signed in
// with, for MethodIdentity, and empty for MethodPassword.
func (s *Store) NewSession(ctx context.Context, userID, method, identityID string, ttl time.Duration) (string, error) {
	token := newID("", 32)
	var identity any
	if identityID != "" {
		identity = identityID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, method, identity_id, expires_at) VALUES (?, ?, ?, ?, ?)`,
		HashToken(token), userID, method, identity, time.Now().Add(ttl).Unix())
	return token, err
}

// SessionUser returns the user a live session token belongs to. A disabled user has no live session.
func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNotFound
	}
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`JOIN sessions s ON s.user_id = u.id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`, HashToken(token), time.Now().Unix()))
}

func (s *Store) EndSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

// EndPasswordSessions ends every session made with a password, when local login is turned off.
func (s *Store) EndPasswordSessions(ctx context.Context) ([]string, error) {
	return s.endSessionsTx(ctx, `method = 'password'`)
}

// EndSessionsNotAt ends every session made with an identity of another issuer than the configured one. It returns those issuers
// and the ended sessions' hashes, read in the same transaction as the sessions are ended.
func (s *Store) EndSessionsNotAt(ctx context.Context, issuer string) (issuers, ended []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT i.issuer FROM sessions s JOIN user_identities i ON i.id = s.identity_id WHERE i.issuer != ?`, issuer)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var i string
		if err := rows.Scan(&i); err != nil {
			rows.Close()
			return nil, nil, err
		}
		issuers = append(issuers, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if ended, err = endSessions(ctx, tx, `identity_id IN (SELECT id FROM user_identities WHERE issuer != ?)`, issuer); err != nil {
		return nil, nil, err
	}
	return issuers, ended, tx.Commit()
}

func (s *Store) endSessionsTx(ctx context.Context, where string, args ...any) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ended, err := endSessions(ctx, tx, where, args...)
	if err != nil {
		return nil, err
	}
	return ended, tx.Commit()
}

// endSessions deletes the sessions that match where and returns their hashes.
func endSessions(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM sessions WHERE `+where+` RETURNING token_hash`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ended []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		ended = append(ended, h)
	}
	return ended, rows.Err()
}

// PurgeSessions drops the expired ones.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// --- audit -------------------------------------------------------------------------------------------------------------------

// AuditEntry is one thing somebody did, or was refused.
type AuditEntry struct {
	ID             int64
	At             time.Time
	Actor, Action  string
	Project, Agent string
	Detail         string
	Allowed        bool
}

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit (at, actor, action, project, agent, detail, allowed) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Actor, e.Action, e.Project, e.Agent, e.Detail, boolInt(e.Allowed))
	return err
}

// AuditOf lists the latest entries about a project, newest first.
func (s *Store) AuditOf(ctx context.Context, project string, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, actor, action, project, agent, detail, allowed FROM audit
		WHERE project = ? ORDER BY id DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var allowed int
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Project, &e.Agent, &e.Detail, &allowed); err != nil {
			return nil, err
		}
		e.At, e.Allowed = time.Unix(at, 0), allowed == 1
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
