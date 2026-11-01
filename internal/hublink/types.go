// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"errors"
	"io"
	"sort"
	"time"

	"github.com/egzo-ai/egzo/internal/proxy"
)

// ErrNoProject is returned for a project the engine does not have (any more).
type ErrNoProject struct{ Name string }

func (e ErrNoProject) Error() string { return "no project " + e.Name }

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
	Exposed  []Exposure
}

// What a declared port speaks. A hub serves `http` as a preview; `tcp` and `udp` are local only.
const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
)

// Where an exposed port is open. A hub serves only the ones open for `hub`: a port open for `local` is on the machine of the project.
const (
	WhereHub   = "hub"
	WhereLocal = "local"
)

// MaxExposures is how many ports one agent declares, and so how many it can have open: they go out in every status line.
const MaxExposures = 32

// Exposure is a port an agent has open. The protocol says what is behind it, and Where for whom.
type Exposure struct {
	Port     int
	Name     string
	Protocol string
	Where    string
	ID       string // made by the control sidecar for a port open for the hub, the same each time this instance exposes this port
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

// ErrStreamEnded is returned when a stream ended without being asked to: the container stopped, or the connection dropped.
var ErrStreamEnded = errors.New("the stream ended")
