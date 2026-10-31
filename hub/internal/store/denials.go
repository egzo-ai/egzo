package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Denial is a group of the refusals and failures a project's proxy recorded for one instance: one host, one protocol, one
// reason code.
type Denial struct {
	Project         string
	Instance        string // the instance's id; empty when the proxy could not tell who it was
	ClaimedAgent    string // the name the client sent in its proxy credentials: part of the group, so wrong ones are told apart by name
	Client          string // the address of the latest
	UserAgent       string // what the latest client said it was
	Protocol, Host  string
	Code            string
	Event, Decision string
	Agent, Actor    string
	Profile, Server string
	Text            string
	Count           int
	First, Last     time.Time
	Fixed           time.Time // zero while it is not
}

// MaxDenials is how many groups a project keeps; the ones seen least recently go first.
const MaxDenials = 500

// RecordDenials adds what was seen to the groups: their counts grow, and a group that was fixed is open again.
func (s *Store) RecordDenials(ctx context.Context, seen []Denial) error {
	if len(seen) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	projects := map[string]bool{}
	for _, d := range seen {
		_, err := tx.ExecContext(ctx, `INSERT INTO denials
			(project, instance, claimed_agent, protocol, host, code, event, decision, agent, client, user_agent, actor, profile, server, text,
				count, first_at, last_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project, instance, claimed_agent, protocol, host, code) DO UPDATE SET
				event = excluded.event, decision = excluded.decision, agent = excluded.agent, client = excluded.client, user_agent = excluded.user_agent,
				actor = CASE WHEN excluded.actor = '' THEN denials.actor ELSE excluded.actor END,
				profile = excluded.profile, server = excluded.server, text = excluded.text, count = denials.count + excluded.count,
				first_at = MIN(denials.first_at, excluded.first_at), last_at = MAX(denials.last_at, excluded.last_at), fixed_at = NULL`,
			d.Project, d.Instance, d.ClaimedAgent, d.Protocol, d.Host, d.Code, d.Event, d.Decision, d.Agent, d.Client, d.UserAgent, d.Actor,
			d.Profile, d.Server, d.Text, d.Count, d.First.UnixMilli(), d.Last.UnixMilli())
		if err != nil {
			return err
		}
		projects[d.Project] = true
	}
	for p := range projects {
		if _, err := tx.ExecContext(ctx, `DELETE FROM denials WHERE project = ? AND rowid NOT IN
			(SELECT rowid FROM denials WHERE project = ? ORDER BY last_at DESC LIMIT ?)`, p, p, MaxDenials); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FixDenials marks the open groups of an instance's host and protocol for one of the steps as fixed: the instance got through at
// at, after them.
func (s *Store) FixDenials(ctx context.Context, project, instance, protocol, host string, events []string, at time.Time) error {
	for _, event := range events {
		_, err := s.db.ExecContext(ctx, `UPDATE denials SET fixed_at = ? WHERE project = ? AND instance = ? AND protocol = ?
			AND host = ? AND event = ? AND fixed_at IS NULL AND last_at <= ?`,
			at.UnixMilli(), project, instance, protocol, host, event, at.UnixMilli())
		if err != nil {
			return err
		}
	}
	return nil
}

// Denials lists a project's groups, the most recently seen first.
func (s *Store) Denials(ctx context.Context, project string) ([]Denial, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project, instance, claimed_agent, protocol, host, code, event, decision, agent, client,
		user_agent, actor, profile, server, text, count, first_at, last_at, fixed_at FROM denials WHERE project = ? ORDER BY last_at DESC, host`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Denial
	for rows.Next() {
		var d Denial
		var first, last int64
		var fixed sql.NullInt64
		if err := rows.Scan(&d.Project, &d.Instance, &d.ClaimedAgent, &d.Protocol, &d.Host, &d.Code, &d.Event, &d.Decision, &d.Agent,
			&d.Client, &d.UserAgent, &d.Actor, &d.Profile, &d.Server, &d.Text, &d.Count, &first, &last, &fixed); err != nil {
			return nil, err
		}
		d.First, d.Last = time.UnixMilli(first), time.UnixMilli(last)
		if fixed.Valid {
			d.Fixed = time.UnixMilli(fixed.Int64)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PurgeDenials removes the groups last seen before.
func (s *Store) PurgeDenials(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM denials WHERE last_at < ?`, before.UnixMilli())
	return err
}

// AuditCursor is the time of the last event the hub handled from a project's audit trail; zero when it never read it.
func (s *Store) AuditCursor(ctx context.Context, project string) (time.Time, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT at FROM audit_cursors WHERE project = ?`, project).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, at).UTC(), nil
}

// SetAuditCursor records how far a project's audit trail was read.
func (s *Store) SetAuditCursor(ctx context.Context, project string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_cursors (project, at) VALUES (?, ?)
		ON CONFLICT (project) DO UPDATE SET at = MAX(audit_cursors.at, excluded.at)`, project, at.UnixNano())
	return err
}
