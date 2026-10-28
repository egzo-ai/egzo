package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The states of a task.
const (
	TaskRunning     = "running"
	TaskDone        = "done"
	TaskFailed      = "failed"
	TaskInterrupted = "interrupted" // it ran when the hub stopped
)

// Task is one action of the hub on an engine (a spawn, a start, a stop, a removal), from when it was asked for until it ended.
type Task struct {
	ID              string
	Verb            string // spawn, start, stop or rm
	Engine, Project string
	Agent           string
	Template        string // what a spawn makes the agent from
	Actor           string // who asked (rbac.Actor)
	State           string
	Step            string // what it does now, or did last
	Err             string // why it failed
	Started, Ended  time.Time
	// Dismissed: somebody read the failure, and it is no longer shown on its agent.
	Dismissed bool
}

// Over reports whether the task has ended, well or not.
func (t Task) Over() bool { return t.State != TaskRunning }

// ErrNoTask is returned for a task that does not exist.
var ErrNoTask = errors.New("no such task")

const taskColumns = `id, verb, engine, project, agent, template, actor, state, step, error, started_at, ended_at, dismissed`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var started int64
	var ended sql.NullInt64
	var dismissed int
	if err := row.Scan(&t.ID, &t.Verb, &t.Engine, &t.Project, &t.Agent, &t.Template, &t.Actor, &t.State, &t.Step, &t.Err, &started, &ended, &dismissed); err != nil {
		return Task{}, err
	}
	t.Started, t.Dismissed = time.UnixMilli(started), dismissed == 1
	if ended.Valid {
		t.Ended = time.UnixMilli(ended.Int64)
	}
	return t, nil
}

func queryTasks(ctx context.Context, db *sql.DB, query string, args ...any) ([]Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// AddTask records a task that has started; a new one gets an id.
func (s *Store) AddTask(ctx context.Context, t Task) (Task, error) {
	if t.ID == "" {
		t.ID = newID("tk", 8)
	}
	if t.State == "" {
		t.State = TaskRunning
	}
	if t.Started.IsZero() {
		t.Started = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (`+taskColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0)`,
		t.ID, t.Verb, t.Engine, t.Project, t.Agent, t.Template, t.Actor, t.State, t.Step, t.Err, t.Started.UnixMilli())
	return t, err
}

// SetTaskStep records what a running task does now.
func (s *Store) SetTaskStep(ctx context.Context, id, step string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET step = ? WHERE id = ? AND state = ?`, step, id, TaskRunning)
	return err
}

// EndTask records how a running task ended: state done or failed, and why it failed.
func (s *Store) EndTask(ctx context.Context, id, state, why string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = ?, error = ?, ended_at = ? WHERE id = ? AND state = ?`, state, why, at.UnixMilli(), id, TaskRunning)
	return err
}

// InterruptTasks marks the tasks still running as interrupted: the hub stopped while they ran, and nothing follows them any more.
func (s *Store) InterruptTasks(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = ?, error = ?, ended_at = ? WHERE state = ?`,
		TaskInterrupted, "the hub stopped while it ran: look at the agent to see where it got", at.UnixMilli(), TaskRunning)
	return err
}

// DismissTask takes an ended task off its agent; it stays in the project's activity.
func (s *Store) DismissTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET dismissed = 1 WHERE id = ? AND state != ?`, id, TaskRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoTask
	}
	return nil
}

// TaskByID reads one task.
func (s *Store) TaskByID(ctx context.Context, id string) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNoTask
	}
	return t, err
}

// OpenTasks lists, oldest first, the tasks that are shown on their agents: those that run, and those that failed or were
// interrupted and that nobody dismissed.
func (s *Store) OpenTasks(ctx context.Context) ([]Task, error) {
	return queryTasks(ctx, s.db, `WHERE state = ? OR (state != ? AND dismissed = 0) ORDER BY started_at, id`, TaskRunning, TaskDone)
}

// TasksOf lists the latest tasks of a project, or of one agent of it, newest first.
func (s *Store) TasksOf(ctx context.Context, engine, project, agent string, limit int) ([]Task, error) {
	if agent != "" {
		return queryTasks(ctx, s.db, `WHERE engine = ? AND project = ? AND agent = ? ORDER BY started_at DESC, id DESC LIMIT ?`, engine, project, agent, limit)
	}
	return queryTasks(ctx, s.db, `WHERE engine = ? AND project = ? ORDER BY started_at DESC, id DESC LIMIT ?`, engine, project, limit)
}

// PurgeTasks removes the tasks that ended before, except a failure nobody dismissed.
func (s *Store) PurgeTasks(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE ended_at < ? AND (state = ? OR dismissed = 1)`, before.UnixMilli(), TaskDone)
	return err
}
