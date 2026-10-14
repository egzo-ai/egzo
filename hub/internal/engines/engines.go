// Package engines is the seam between the hub and the container engines it manages. The hub's own logic (discovery, orphan
// claims, RBAC, fan-out over several engines, an engine going down) is written against the Engine interface and tested with a
// fake; Docker is the one real implementation, built on core's packages.
package engines

import (
	"context"
	"io"
	"time"
)

// Engine is one connected container engine, as the hub uses it.
type Engine interface {
	// Projects lists every egzo project on the engine, with its agents.
	Projects(ctx context.Context) ([]Project, error)
	// Project reads one project, with what its control sidecar reports about its agents. ErrNoProject when it is gone.
	Project(ctx context.Context, name string) (Project, error)
	// Spawn makes an instance of a template; actor is the `actor` label it gets ("user:<id>").
	Spawn(ctx context.Context, project, template, name, actor string) (agent string, err error)
	Start(ctx context.Context, project, agent string) error
	Stop(ctx context.Context, project, agent string) error
	Remove(ctx context.Context, project, agent string) error
	// Attach connects a terminal to an agent's session until it ends or ctx is cancelled.
	Attach(ctx context.Context, project, agent string, term Terminal) error
	// Events returns the latest events of a project's control sidecar (message texts left out), oldest first.
	Events(ctx context.Context, project, agent string, limit int) ([]Event, error)
	// Logs returns the end of an agent's, or a sidecar's, container log.
	Logs(ctx context.Context, project, container string, tail int) (string, error)
	// Spec returns the project's published templates as indented JSON, without any secret value.
	Spec(ctx context.Context, project string) ([]byte, error)
	Close() error
}

// ErrNoProject is returned for a project the engine does not have (any more).
type ErrNoProject struct{ Name string }

func (e ErrNoProject) Error() string { return "no project " + e.Name + " on this engine" }

// Project is what discovery learns about one project from its labels and its control sidecar.
type Project struct {
	Name string
	// Dir is where the project file was, as the labels say; informational.
	Dir string
	// Up says the control sidecar and the proxy are running, which is what spawning needs.
	Up        bool
	Services  []Service
	Agents    []Agent
	Templates []string
	CreatedBy string
}

// Service is a sidecar: control or proxy.
type Service struct {
	Name, State, Health string
}

// Agent is one instance.
type Agent struct {
	Name     string // the instance, what every command takes
	Template string
	State    string // running, exited, created...
	Health   string
	// Activity is what the agent reports: working, idle, waiting..., or stopped / starting.
	Activity string
	Status   string
	Open     int
	Waiting  bool   // it asked a question nobody answered
	Actor    string // the `actor` label: who spawned it
	Stale    bool
	Created  time.Time
}

// Running reports whether the agent's container is running.
func (a Agent) Running() bool { return a.State == "running" }

// Event is one entry of the control sidecar's event stream, as the web UI shows it.
type Event struct {
	Seq   int
	Time  time.Time
	Type  string
	Agent string
	Actor string
	Text  string
}

// Size is a terminal's size.
type Size struct{ Cols, Rows uint }

// Terminal is the client side of an attach: what the user types, where the output goes, and the size.
type Terminal struct {
	In     io.Reader
	Out    io.Writer
	Size   Size
	Resize <-chan Size
}
