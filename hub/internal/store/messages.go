package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Message is one message of a project, as the hub keeps it: what the events feed carried of it.
type Message struct {
	ID, Project   string
	At            time.Time
	From, To      string // agent:<name>, user:<id>, operator or egzo
	Kind          string // request, question, resolution, update
	Subject, Text string
	Re            string
	Choices       []string
	Outcome       string    // a resolution's; for a request or question, how it was resolved
	ResolvedAt    time.Time // a request or question, once resolved; zero while open
}

// KeepMessage records a message. Recording it again (the hub reads an event again after a restart) changes nothing.
func (s *Store) KeepMessage(ctx context.Context, m Message) error {
	choices := ""
	if len(m.Choices) > 0 {
		data, _ := json.Marshal(m.Choices)
		choices = string(data)
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO messages (id, project, at, sender, recipient, kind, subject, text, re, choices, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.Project, m.At.UnixMilli(), m.From, m.To, m.Kind, m.Subject, m.Text, m.Re, choices, m.Outcome)
	return err
}

// MessageResolved records that a request or question of the project was resolved, and how.
func (s *Store) MessageResolved(ctx context.Context, project, id, outcome string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET outcome = ?, resolved_at = ? WHERE id = ? AND project = ? AND resolved_at IS NULL`,
		outcome, at.UnixMilli(), id, project)
	return err
}

// MessageOf is one message of a project, by its id; ErrNotFound when the hub has none.
func (s *Store) MessageOf(ctx context.Context, project, id string) (Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ? AND project = ?`, id, project)
	if err != nil {
		return Message{}, err
	}
	list, err := scanMessages(rows)
	if err != nil {
		return Message{}, err
	}
	if len(list) == 0 {
		return Message{}, ErrNotFound
	}
	return list[0], nil
}

// MessagesOf lists what an address of the project sent and was sent, newest first, at most limit; before, when not zero, lists only what
// came before it.
func (s *Store) MessagesOf(ctx context.Context, project, address string, before time.Time, limit int) ([]Message, error) {
	until := int64(1) << 62
	if !before.IsZero() {
		until = before.UnixMilli()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE project = ? AND (sender = ? OR recipient = ?) AND at < ?
		ORDER BY at DESC, id DESC LIMIT ?`, project, address, address, until, limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

const messageColumns = `id, project, at, sender, recipient, kind, subject, text, re, choices, outcome, resolved_at`

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at int64
		var choices string
		var resolved sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Project, &at, &m.From, &m.To, &m.Kind, &m.Subject, &m.Text, &m.Re, &choices, &m.Outcome, &resolved); err != nil {
			return nil, err
		}
		m.At = time.UnixMilli(at).UTC()
		if resolved.Valid {
			m.ResolvedAt = time.UnixMilli(resolved.Int64).UTC()
		}
		if choices != "" && json.Unmarshal([]byte(choices), &m.Choices) != nil {
			return nil, errors.New("a message's choices are not JSON")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
