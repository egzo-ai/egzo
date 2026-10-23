// Package engines is the seam between the hub and the container engines it manages. The hub's own logic (discovery, the operators'
// access, RBAC, fan-out over several engines, an engine going down) is written against the Engine interface and tested with a
// fake; Docker is the one real implementation, built on core's packages.
package engines

import (
	"context"
	"io"
	"sort"
	"time"

	"github.com/egzo-ai/egzo/internal/proxy"
)

// Engine is one connected container engine, as the hub uses it.
type Engine interface {
	// Projects lists every egzo project on the engine, with its agents.
	Projects(ctx context.Context) ([]Project, error)
	// Project reads one project from the engine's containers. It does not ask the control sidecar anything, so it is cheap
	// enough to call on every page view: what the agents are doing comes from FollowStatus, and Activity, Status, Open and
	// Waiting of its agents are empty. ErrNoProject when it is gone.
	Project(ctx context.Context, name string) (Project, error)
	// FollowStatus follows the status lines the project's control sidecar writes to its log (core's status log), through the
	// engine's own log API, with no command run in the sidecar. It calls on for each line, oldest first, beginning with the
	// latest one already in the log, and returns when ctx ends or the stream breaks; a stream that cannot be opened or read
	// is an error that says why. The caller retries.
	FollowStatus(ctx context.Context, project string, on func(StatusLine)) error
	// FollowAudit follows the audit trail the project's proxy writes to its log (core's proxy events), through the engine's log
	// API, like FollowStatus. It begins with the events written after since, or with the last AuditBacklog lines of the log when
	// since is zero, and returns when ctx ends or the stream breaks.
	FollowAudit(ctx context.Context, project string, since time.Time, on func(AuditEvent)) error
	// Watch calls changed when the egzo containers of the engine change (made, started, stopped, removed), and returns when
	// ctx ends or the event stream breaks. It is how the hub knows to look again without asking on a timer.
	Watch(ctx context.Context, changed func()) error
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
	// Published reads the templates the project's control sidecar holds, which a command in the sidecar answers: ask rarely.
	Published(ctx context.Context, project string) (Published, error)
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
	// Ignored is what carries the project's label with labels egzo never writes (engine.IdentityOf): not the project's, and not
	// shown as part of it. Each says what and why.
	Ignored []string `json:",omitempty"`
}

// Published is what a project's control sidecar holds of its templates: the names, and a hash of each, which an instance
// carries from when it was spawned. An instance whose template hash differs, or whose template is gone, is stale.
type Published struct {
	Templates map[string]string // name -> hash
}

// Names lists the templates, sorted.
func (p Published) Names() []string {
	names := make([]string, 0, len(p.Templates))
	for name := range p.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Service is a sidecar: control or proxy.
type Service struct {
	Name, State, Health string
	// ID is the container's id: a sidecar that was made again is another container.
	ID string
}

// Agent is one instance.
type Agent struct {
	Name     string // the instance, what every command takes
	ID       string // the instance's id: a name can come back, an id does not
	Template string
	State    string // running, exited, created...
	Health   string
	// Activity is what the agent reports: starting, idle, working, blocked; stopped when its container is not running. It
	// is empty in what an Engine returns for a running agent (the hub fills it from the status feed).
	Activity string
	Status   string
	Open     int
	Waiting  bool   // it asked a question nobody answered
	Actor    string // the `actor` label: who spawned it
	Stale    bool
	Created  time.Time
	// TemplateHash is the hash of the template the instance was spawned from (a label), to compare with Published.
	TemplateHash string
}

// Running reports whether the agent's container is running.
func (a Agent) Running() bool { return a.State == "running" }

// AuditEvent is one event of a proxy's audit trail: a step of one connection and what the proxy decided (see
// roadmap/proxy-audit.md).
type AuditEvent = proxy.Event

// AuditBacklog is how many lines of a proxy's log FollowAudit reads first when it has no time to start from.
const AuditBacklog = 2000

// StatusLine is one status line of the control sidecar's log: what every agent of the project was doing at Time.
type StatusLine struct {
	Time   time.Time
	Reason string // start, change or heartbeat
	// Interval is how often the sidecar writes a heartbeat; a reader that has heard nothing for a few of them has lost it.
	Interval time.Duration
	Agents   []AgentStatus
}

// AgentStatus is what the control sidecar knows of one agent.
type AgentStatus struct {
	Agent    string
	Status   string
	Activity string // starting, idle, working or blocked
	Open     int
	Waiting  bool
	Updated  time.Time
}

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
