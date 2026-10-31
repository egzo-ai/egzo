// Package engines is the seam between the hub and the container engines it manages. The hub's own logic (discovery, the operators'
// access, RBAC, fan-out over several engines, an engine going down) is written against the Engine interface and tested with a
// fake; Docker is the one real implementation, built on core's packages.
package engines

import (
	"context"
	"time"

	"github.com/egzo-ai/egzo/internal/hublink"
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
	// Spawn makes an instance of a template; actor is the `actor` label it gets ("user:<id>"). It calls progress with each step it
	// takes ("create container ..."), as `egzo spawn` prints them.
	Spawn(ctx context.Context, project, template, name, actor string, progress func(step string)) (agent string, err error)
	Start(ctx context.Context, project, agent string) error
	Stop(ctx context.Context, project, agent string) error
	// Remove removes an instance and what it owns, calling progress with each step, as `egzo agent rm` prints them.
	Remove(ctx context.Context, project, agent string, progress func(step string)) error
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

// What an engine says is what a link says: the same types, in core.
type (
	ErrNoProject = hublink.ErrNoProject
	Project      = hublink.Project
	Published    = hublink.Published
	Service      = hublink.Service
	Agent        = hublink.Agent
	AuditEvent   = hublink.AuditEvent
	StatusLine   = hublink.StatusLine
	AgentStatus  = hublink.AgentStatus
	Event        = hublink.Event
	Size         = hublink.Size
	Terminal     = hublink.Terminal
)

// AuditBacklog is how many lines of a proxy's log FollowAudit reads first when it has no time to start from.
const AuditBacklog = hublink.AuditBacklog

// ErrStreamEnded is returned when an engine's stream ended without being asked to.
var ErrStreamEnded = hublink.ErrStreamEnded
