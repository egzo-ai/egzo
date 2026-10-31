// Package links is the seam between the hub and the projects that linked to it. The hub's own logic (registrations, the operators'
// access, RBAC, a project going offline) is written against Engine and Conn and tested with a fake; a real link is core's hublink.Peer.
package links

import (
	"context"

	"github.com/egzo-ai/egzo/internal/hublink"
)

// Engine is what the hub asks of one linked project. It is the project's own verbs, and nothing else: the hub holds no way to run a
// container, mount a path or exec a command of its choosing.
type Engine interface {
	// Project reads the project from its containers. It runs no command in the control sidecar. ErrNoProject when it is gone.
	Project(ctx context.Context) (Project, error)
	// Published reads the templates the project's control sidecar holds, which a command in the sidecar answers.
	Published(ctx context.Context) (Published, error)
	// Spawn makes an instance of a template; actor is the `actor` label it gets ("user:<id>"). It calls progress with each step it
	// takes ("create container ..."), as `egzo spawn` prints them.
	Spawn(ctx context.Context, template, name, actor string, progress func(step string)) (agent string, err error)
	Start(ctx context.Context, agent string) error
	Stop(ctx context.Context, agent string) error
	// Remove removes an instance and what it owns, calling progress with each step, as `egzo agent rm` prints them.
	Remove(ctx context.Context, agent string, progress func(step string)) error
	// Attach connects a terminal to an agent's session until it ends or ctx is cancelled.
	Attach(ctx context.Context, agent string, term Terminal) error
	// Events returns the latest events of the project's control sidecar (message texts left out), oldest first.
	Events(ctx context.Context, agent string, limit int) ([]Event, error)
	// Logs returns the end of an agent's, or a sidecar's, container log.
	Logs(ctx context.Context, container string, tail int) (string, error)
	// Spec returns the project's published templates as indented JSON, without any secret value.
	Spec(ctx context.Context) ([]byte, error)
}

// Conn is a link that has proved its key: the project's verbs, and the connection they travel on.
type Conn interface {
	Engine
	// Send tells the link where its registration stands.
	Send(Verdict) error
	// Run takes what the link pushes until the connection ends.
	Run(ctx context.Context, handlers Handlers) error
	// Closed is closed when the connection has ended.
	Closed() <-chan struct{}
	Close() error
	// Finish ends the connection after its last verdict: it lets the link hang up, and closes it if it does not.
	Finish() error
}

// What a link says: the same types, in core.
type (
	ErrNoProject  = hublink.ErrNoProject
	Project       = hublink.Project
	ProjectUpdate = hublink.ProjectUpdate
	Published     = hublink.Published
	Service       = hublink.Service
	Agent         = hublink.Agent
	AuditEvent    = hublink.AuditEvent
	StatusLine    = hublink.StatusLine
	AgentStatus   = hublink.AgentStatus
	Event         = hublink.Event
	Size          = hublink.Size
	Terminal      = hublink.Terminal
	Hello         = hublink.Hello
	Verdict       = hublink.Verdict
	Handlers      = hublink.Handlers
)

// AuditBacklog is how many lines of a proxy's log a link reads first when the hub has no time to start from.
const AuditBacklog = hublink.AuditBacklog

var _ Conn = (*hublink.Peer)(nil)
