package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EventCursor is the sequence number of the last event the hub handled from a project's events feed; -1 when it never read it.
func (s *Store) EventCursor(ctx context.Context, project string) (int, error) {
	var seq int
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM event_cursors WHERE project = ?`, project).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return seq, err
}

// SetEventCursor records how far a project's events were read. It can go back: a project whose control volume was made anew numbers its
// events from one again.
func (s *Store) SetEventCursor(ctx context.Context, project string, seq int) error {
	return setEventCursor(ctx, s.db, project, seq)
}

func setEventCursor(ctx context.Context, db execer, project string, seq int) error {
	_, err := db.ExecContext(ctx, `INSERT INTO event_cursors (project, seq) VALUES (?, ?)
		ON CONFLICT (project) DO UPDATE SET seq = excluded.seq`, project, seq)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Outgoing is one entry of the outbox: what an agent and a user said to each other, as the bridges read it.
type Outgoing struct {
	Seq     int64
	At      time.Time
	Project string
	Agent   string
	Body    []byte
}

// RecordEvent moves a project's event cursor to seq and, in the same step, puts what the event says in the outbox when body is not nil: an
// event is either handled or read again. It returns the outbox entry's seq, zero when there is none.
func (s *Store) RecordEvent(ctx context.Context, project string, seq int, agent string, body []byte, at time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var out int64
	if body != nil {
		result, err := tx.ExecContext(ctx, `INSERT INTO outbox (at, project, agent, body) VALUES (?, ?, ?, ?)`, at.UnixMilli(), project, agent, string(body))
		if err != nil {
			return 0, err
		}
		if out, err = result.LastInsertId(); err != nil {
			return 0, err
		}
	}
	if err := setEventCursor(ctx, tx, project, seq); err != nil {
		return 0, err
	}
	return out, tx.Commit()
}

// OutboxAfter lists the outbox after seq, oldest first, at most limit entries.
func (s *Store) OutboxAfter(ctx context.Context, after int64, limit int) ([]Outgoing, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, at, project, agent, body FROM outbox WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outgoing
	for rows.Next() {
		var o Outgoing
		var at int64
		var body string
		if err := rows.Scan(&o.Seq, &at, &o.Project, &o.Agent, &body); err != nil {
			return nil, err
		}
		o.At, o.Body = time.UnixMilli(at).UTC(), []byte(body)
		out = append(out, o)
	}
	return out, rows.Err()
}

// OutboxLast is the seq of the newest entry the outbox ever had; zero when it never had one.
func (s *Store) OutboxLast(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'outbox'`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq.Int64, err
}

// PurgeOutbox drops what is older than before.
func (s *Store) PurgeOutbox(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE at < ?`, before.UnixMilli())
	return err
}
