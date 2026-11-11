// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discordbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"

	"github.com/egzo-ai/egzo/hub/internal/discord"
)

// The bridge's own state, in SQLite on its volume. The hub knows none of it: which role is which agent, the channels each is in, and
// which Discord message is which egzo message, so that a reply or a click goes back to the right one.

const schema = `
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS cursor (id INTEGER PRIMARY KEY CHECK (id = 1), seq INTEGER NOT NULL);
-- An agent added to a guild: the role people mention to address it.
CREATE TABLE IF NOT EXISTS agents (
	guild TEXT NOT NULL,
	role TEXT NOT NULL,
	project TEXT NOT NULL,
	agent TEXT NOT NULL,
	name TEXT NOT NULL,
	gone INTEGER NOT NULL DEFAULT 0,    -- when the agent was first known to have left its project (unix ms), 0 while it is there
	PRIMARY KEY (guild, role),
	UNIQUE (guild, project, agent)
);
-- The channels an agent is in.
CREATE TABLE IF NOT EXISTS places (
	guild TEXT NOT NULL,
	role TEXT NOT NULL,
	channel TEXT NOT NULL,
	PRIMARY KEY (guild, role, channel)
);
-- A Discord message and the egzo message it is: one a person sent to an agent, or one the bot posted for an agent to a person.
CREATE TABLE IF NOT EXISTS posts (
	message TEXT PRIMARY KEY,
	channel TEXT NOT NULL,
	place TEXT NOT NULL,                -- the channel the agent is in: the channel, or a thread's parent
	guild TEXT NOT NULL,
	project TEXT NOT NULL,
	agent TEXT NOT NULL,
	egzo TEXT NOT NULL,                 -- the egzo message's id
	person TEXT NOT NULL,               -- the person's Discord id: who wrote it, or whom the agent's post is for
	kind TEXT NOT NULL,                 -- request (a person's), question, resolution, update, request (an agent's)
	choices TEXT NOT NULL DEFAULT '[]',
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS posts_egzo ON posts (egzo);
CREATE INDEX IF NOT EXISTS posts_person ON posts (person, project, agent, at);
`

// Agent is an agent added to a guild.
type Agent struct {
	Guild, Role          discord.Snowflake
	Project, Agent, Name string
	// Gone is when the agent was first known to have left its project, zero while it is there. Its role is kept for a while: an agent
	// re-created under the same name gets it back.
	Gone time.Time
}

// Post is a Discord message that is an egzo message.
type Post struct {
	Message, Channel, Guild discord.Snowflake
	Place                   discord.Snowflake // the channel the agent is in: Channel, or the parent of the thread Channel is
	Project, Agent, Egzo    string
	Person                  discord.Snowflake // who wrote it, or whom the agent's post is for
	Kind                    string
	Choices                 []string
}

// State is the bridge's database.
type State struct{ db *sql.DB }

// OpenState opens the database at path, making it if needed.
func OpenState(path string) (*State, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &State{db: db}, nil
}

func (s *State) Close() error { return s.db.Close() }

// Cursor is the seq of the last outbox event handled, -1 when none ever was: only what comes next.
func (s *State) Cursor(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM cursor WHERE id = 1`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return seq, err
}

func (s *State) SetCursor(ctx context.Context, seq int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cursor (id, seq) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET seq = excluded.seq`, seq)
	return err
}

// AddAgent records an agent's role in a guild.
func (s *State) AddAgent(ctx context.Context, a Agent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (guild, role, project, agent, name) VALUES (?, ?, ?, ?, ?)`,
		a.Guild, a.Role, a.Project, a.Agent, a.Name)
	return err
}

const agentSelect = `SELECT guild, role, project, agent, name, gone FROM agents `

func scanAgents(rows *sql.Rows, err error) ([]Agent, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		var gone int64
		if err := rows.Scan(&a.Guild, &a.Role, &a.Project, &a.Agent, &a.Name, &gone); err != nil {
			return nil, err
		}
		if gone != 0 {
			a.Gone = time.UnixMilli(gone)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentInGuild is the role an agent has in a guild, if it was added there.
func (s *State) AgentInGuild(ctx context.Context, guild discord.Snowflake, project, agent string) (Agent, bool, error) {
	list, err := scanAgents(s.db.QueryContext(ctx, agentSelect+`WHERE guild = ? AND project = ? AND agent = ?`, guild, project, agent))
	if err != nil || len(list) == 0 {
		return Agent{}, false, err
	}
	return list[0], true, nil
}

// AgentByRole is the agent a role is.
func (s *State) AgentByRole(ctx context.Context, guild, role discord.Snowflake) (Agent, bool, error) {
	list, err := scanAgents(s.db.QueryContext(ctx, agentSelect+`WHERE guild = ? AND role = ?`, guild, role))
	if err != nil || len(list) == 0 {
		return Agent{}, false, err
	}
	return list[0], true, nil
}

// AgentEverywhere lists the guilds an agent is in.
func (s *State) AgentEverywhere(ctx context.Context, project, agent string) ([]Agent, error) {
	return scanAgents(s.db.QueryContext(ctx, agentSelect+`WHERE project = ? AND (? = '' OR agent = ?)`, project, agent, agent))
}

// AgentsIn lists the agents that are in a channel.
func (s *State) AgentsIn(ctx context.Context, guild, channel discord.Snowflake) ([]Agent, error) {
	return scanAgents(s.db.QueryContext(ctx, `SELECT a.guild, a.role, a.project, a.agent, a.name, a.gone FROM agents a
		JOIN places p ON p.guild = a.guild AND p.role = a.role WHERE a.guild = ? AND p.channel = ? ORDER BY a.name`, guild, channel))
}

// MarkGone records that an agent left its project, in every guild, unless it was known already: the first time is when its roles go. An
// empty agent is all of the project's.
func (s *State) MarkGone(ctx context.Context, project, agent string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET gone = ? WHERE project = ? AND (? = '' OR agent = ?) AND gone = 0`, at.UnixMilli(), project, agent, agent)
	return err
}

// MarkBack records that an agent is in its project again: re-created under the same name, it keeps its roles and channels.
func (s *State) MarkBack(ctx context.Context, project, agent string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET gone = 0 WHERE project = ? AND agent = ? AND gone != 0`, project, agent)
	return err
}

// KeptAgents lists every agent that has a role somewhere, once: what to ask the hub about.
func (s *State) KeptAgents(ctx context.Context) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project, agent FROM agents ORDER BY project, agent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var pa [2]string
		if err := rows.Scan(&pa[0], &pa[1]); err != nil {
			return nil, err
		}
		out = append(out, pa)
	}
	return out, rows.Err()
}

// GoneBefore lists the roles of agents that left their project before a time.
func (s *State) GoneBefore(ctx context.Context, before time.Time) ([]Agent, error) {
	return scanAgents(s.db.QueryContext(ctx, agentSelect+`WHERE gone != 0 AND gone < ?`, before.UnixMilli()))
}

// AddPlace puts an agent in a channel.
func (s *State) AddPlace(ctx context.Context, guild, role, channel discord.Snowflake) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO places (guild, role, channel) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, guild, role, channel)
	return err
}

// InPlace reports whether an agent is in a channel.
func (s *State) InPlace(ctx context.Context, guild, role, channel discord.Snowflake) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM places WHERE guild = ? AND role = ? AND channel = ?`, guild, role, channel).Scan(&n)
	return n > 0, err
}

// RemovePlace takes an agent out of a channel, and returns how many channels it is still in.
func (s *State) RemovePlace(ctx context.Context, guild, role, channel discord.Snowflake) (int, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM places WHERE guild = ? AND role = ? AND channel = ?`, guild, role, channel); err != nil {
		return 0, err
	}
	var left int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM places WHERE guild = ? AND role = ?`, guild, role).Scan(&left)
	return left, err
}

// RemoveChannel takes every agent out of a channel the bot can no longer use, and returns the roles left in no channel.
func (s *State) RemoveChannel(ctx context.Context, guild, channel discord.Snowflake) ([]discord.Snowflake, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role FROM places WHERE guild = ? AND channel = ?`, guild, channel)
	if err != nil {
		return nil, err
	}
	var roles []discord.Snowflake
	for rows.Next() {
		var r discord.Snowflake
		if rows.Scan(&r) == nil {
			roles = append(roles, r)
		}
	}
	rows.Close()
	var empty []discord.Snowflake
	for _, r := range roles {
		left, err := s.RemovePlace(ctx, guild, r, channel)
		if err != nil {
			return nil, err
		}
		if left == 0 {
			empty = append(empty, r)
		}
	}
	return empty, nil
}

// RemoveAgent forgets an agent's role in a guild, and its channels.
func (s *State) RemoveAgent(ctx context.Context, guild, role discord.Snowflake) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM places WHERE guild = ? AND role = ?`, guild, role); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE guild = ? AND role = ?`, guild, role)
	return err
}

// AddPost records that a Discord message is an egzo message.
func (s *State) AddPost(ctx context.Context, p Post) error {
	choices, _ := json.Marshal(p.Choices)
	_, err := s.db.ExecContext(ctx, `INSERT INTO posts (message, channel, place, guild, project, agent, egzo, person, kind, choices, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (message) DO NOTHING`,
		p.Message, p.Channel, p.Place, p.Guild, p.Project, p.Agent, p.Egzo, p.Person, p.Kind, string(choices), time.Now().UnixMilli())
	return err
}

func scanPost(row *sql.Row) (Post, bool, error) {
	var p Post
	var choices string
	err := row.Scan(&p.Message, &p.Channel, &p.Place, &p.Guild, &p.Project, &p.Agent, &p.Egzo, &p.Person, &p.Kind, &choices)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	_ = json.Unmarshal([]byte(choices), &p.Choices)
	return p, err == nil, err
}

const postSelect = `SELECT message, channel, place, guild, project, agent, egzo, person, kind, choices FROM posts `

// PostOf is the egzo message a Discord message is.
func (s *State) PostOf(ctx context.Context, message discord.Snowflake) (Post, bool, error) {
	return scanPost(s.db.QueryRowContext(ctx, postSelect+`WHERE message = ?`, message))
}

// stillThere keeps the posts whose agent is still in their channel: an agent taken out of a channel posts nothing more there.
const stillThere = `EXISTS (SELECT 1 FROM agents a JOIN places pl ON pl.guild = a.guild AND pl.role = a.role
	WHERE a.guild = posts.guild AND a.project = posts.project AND a.agent = posts.agent AND pl.channel = posts.place)`

// PostFor is the first Discord message that is an egzo message between its agent and a person, in a channel the agent is still in: where
// to answer it. A message the person was not part of is none: an agent may answer one person's message to another.
func (s *State) PostFor(ctx context.Context, egzo string, person discord.Snowflake) (Post, bool, error) {
	return scanPost(s.db.QueryRowContext(ctx, postSelect+`WHERE egzo = ? AND person = ? AND `+stillThere+` ORDER BY at, rowid LIMIT 1`, egzo, person))
}

// LastPost is the latest Discord message between a person and an agent, in a channel the agent is still in: where they last talked.
func (s *State) LastPost(ctx context.Context, person discord.Snowflake, project, agent string) (Post, bool, error) {
	return scanPost(s.db.QueryRowContext(ctx, postSelect+`WHERE person = ? AND project = ? AND agent = ? AND `+stillThere+` ORDER BY at DESC, rowid DESC LIMIT 1`,
		person, project, agent))
}

// PurgePosts forgets posts older than before: a reply to them goes nowhere.
func (s *State) PurgePosts(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM posts WHERE at < ?`, before.UnixMilli())
	return err
}
