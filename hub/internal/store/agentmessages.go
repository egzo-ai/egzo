package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AgentMessage is a message one agent sent another, as the hub keeps it for the Rich UI chat of the agent it was sent to.
type AgentMessage struct {
	ID      string
	At      time.Time
	Project string
	Agent   string // the agent it was sent to
	Body    []byte
}

// KeepAgentMessage keeps a message one agent sent another. Keeping it again (the hub reads an event again after a restart) changes nothing.
func (s *Store) KeepAgentMessage(ctx context.Context, m AgentMessage) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_messages (id, at, project, agent, body) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.At.UnixMilli(), m.Project, m.Agent, string(m.Body))
	return err
}

// AgentMessageOf is a message one agent sent another, by its id; ErrNotFound when the hub has none, or no longer.
func (s *Store) AgentMessageOf(ctx context.Context, id string) (AgentMessage, error) {
	var m AgentMessage
	var at int64
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT id, at, project, agent, body FROM agent_messages WHERE id = ?`, id).Scan(&m.ID, &at, &m.Project, &m.Agent, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.At, m.Body = time.UnixMilli(at).UTC(), []byte(body)
	return m, err
}

// PurgeAgentMessages drops what is older than before.
func (s *Store) PurgeAgentMessages(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_messages WHERE at < ?`, before.UnixMilli())
	return err
}
