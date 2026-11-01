package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// The hub learns what the agents are doing, and what changed, from what each project's link pushes, never by asking on a timer:
//
//   - the project feed: the whole project, each time it changed. The hub keeps the last one, and pages show that (links.go).
//   - the status feed: a line on every change and a heartbeat every interval, from the control sidecar's log. The latest line is what
//     Projects and Project show.
//   - the audit feed: the proxy's trail, kept for the Network view (network.go).
//
// A link opens them again when they break; the hub only follows. See roadmap/hub-link.md.

// FeedOptions tune the feeds; the zero value is the production setting.
type FeedOptions struct {
	// Silent is how long the hub waits for the first status line before saying the sidecar writes none (90 seconds).
	Silent time.Duration
	// Grace is what is added to three heartbeats before a quiet sidecar is called stale (10 seconds); negative for none.
	Grace time.Duration
	// Flush is how often what an audit follower heard is written to the database (1 second).
	Flush time.Duration
}

func (o FeedOptions) withDefaults() FeedOptions {
	if o.Silent <= 0 {
		o.Silent = 90 * time.Second
	}
	if o.Flush <= 0 {
		o.Flush = time.Second
	}
	if o.Grace == 0 {
		o.Grace = 10 * time.Second
	} else if o.Grace < 0 {
		o.Grace = 0
	}
	return o
}

// FeedState says how far the status of a project can be trusted.
type FeedState string

const (
	// FeedLive: status lines are arriving, and the activity of the agents is what they say.
	FeedLive FeedState = "live"
	// FeedConnecting: the hub has not heard from the sidecar yet.
	FeedConnecting FeedState = "connecting"
	// FeedStale: it was heard from, and has gone quiet for longer than a heartbeat allows.
	FeedStale FeedState = "stale"
	// FeedUnavailable: nothing can be read: the sidecar is not running, or writes no status (it is older than this hub).
	FeedUnavailable FeedState = "unavailable"
	// FeedOffline: the project's link is not connected.
	FeedOffline FeedState = "offline"
)

// Feed is the state of a project's status, and why.
type Feed struct {
	State   FeedState
	Message string
}

// ActivityUnknown is what a running agent is doing when the feed cannot say.
const ActivityUnknown = "unknown"

// follower is what the status feed of a connected project has said.
type follower struct {
	since    time.Time // when the connection began
	lastLine time.Time // when the newest line was written, by the hub's clock
	interval time.Duration
	agents   map[string]links.AgentStatus
}

// feedManager holds what the feeds said, by project id.
type feedManager struct {
	hub *Hub
	opt FeedOptions

	mu        sync.Mutex
	followers map[string]*follower
	audits    map[string]*auditFollower
	exposed   map[string]map[string]exposedPort // by project and id. It stays while a project is offline: its addresses stay its own
}

func newFeedManager(h *Hub, opt FeedOptions) *feedManager {
	return &feedManager{hub: h, opt: opt.withDefaults(), followers: map[string]*follower{}, audits: map[string]*auditFollower{}, exposed: map[string]map[string]exposedPort{}}
}

// connected readies the followers of a connection that is served.
func (m *feedManager) connected(c *connection) {
	m.mu.Lock()
	m.followers[c.project] = &follower{since: time.Now(), agents: map[string]links.AgentStatus{}}
	m.mu.Unlock()
	m.startAudit(c)
}

// disconnected is the end of what a project's feeds say. The audit follower stays: what it heard is shown while the project is offline.
func (m *feedManager) disconnected(c *connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.followers, c.project)
	if f := m.audits[c.project]; f != nil {
		f.connected = false
	}
}

// forget drops all that is held of a project that is gone.
func (m *feedManager) forget(project string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.followers, project)
	delete(m.audits, project)
	delete(m.exposed, project)
}

// status keeps what a status line says. A line that came with the log's replay at the start is as old as it was written, and it is not
// allowed to make a silent sidecar look alive.
func (m *feedManager) status(project string, line links.StatusLine) {
	now := time.Now()
	agents := make(map[string]links.AgentStatus, len(line.Agents))
	for _, a := range line.Agents {
		agents[a.Agent] = a
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.followers[project]
	if f == nil {
		return
	}
	age := time.Duration(0)
	if now.Sub(f.since) < 2*time.Second && !line.Time.IsZero() && now.After(line.Time) {
		age = now.Sub(line.Time)
	}
	f.lastLine = now.Add(-age)
	f.interval = line.Interval
	f.agents = agents
	m.exposed[project] = exposedIn(line)
}

// state says how far a follower can be trusted at a moment.
func (m *feedManager) state(f *follower, now time.Time) Feed {
	if f.lastLine.IsZero() {
		if now.Sub(f.since) > m.opt.Silent {
			return Feed{FeedUnavailable, "the control sidecar writes no status: it is older than this hub (run egzo project up again), or its log cannot be read"}
		}
		return Feed{FeedConnecting, "waiting for the control sidecar's first status line"}
	}
	interval := f.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if age := now.Sub(f.lastLine); age > 3*interval+m.opt.Grace {
		return Feed{FeedStale, fmt.Sprintf("no status from the control sidecar for %s", age.Round(time.Second))}
	}
	return Feed{FeedLive, ""}
}

// controlRunning says what the project's sidecars say of the control sidecar. A project not heard of yet is not one that has none.
func controlRunning(p links.Project, known bool) bool {
	if !known {
		return true
	}
	for _, s := range p.Services {
		if s.Name == "control" {
			return s.State == "running"
		}
	}
	return false
}

// overlay returns the agents of a project with what the feed says they are doing, and the state of the feed. It never changes what it is
// given.
func (h *Hub) overlay(reg store.Registration, p links.Project) (links.Project, Feed) {
	_, known := h.knownProject(reg.ID)
	m := h.feeds
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.followers[reg.ID]
	feed := Feed{FeedOffline, ""}
	switch {
	case f == nil:
	case !controlRunning(p, known):
		feed = Feed{FeedUnavailable, "the project's control sidecar is not running"}
	default:
		feed = m.state(f, time.Now())
	}
	agents := make([]links.Agent, len(p.Agents))
	for i, a := range p.Agents {
		agents[i] = a
		switch {
		case !a.Running():
			agents[i].Activity, agents[i].Waiting, agents[i].Status, agents[i].Open = "stopped", false, "", 0
		case f != nil && feed.State == FeedLive:
			if s, ok := f.agents[a.Name]; ok {
				agents[i].Activity, agents[i].Status, agents[i].Open, agents[i].Waiting = s.Activity, s.Status, s.Open, s.Waiting
			} else {
				agents[i].Activity, agents[i].Status, agents[i].Open, agents[i].Waiting = "starting", "", 0, false
			}
			if agents[i].Activity == "" {
				agents[i].Activity = "starting"
			}
		default:
			agents[i].Activity, agents[i].Status, agents[i].Open, agents[i].Waiting = ActivityUnknown, "", 0, false
		}
	}
	p.Agents = agents
	return p, feed
}

// Run keeps the database tidy until ctx ends. The links do the rest: they dial, and they push.
func (h *Hub) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		now := time.Now()
		_ = h.store.PurgeSessions(ctx)
		_ = h.store.PurgeDenials(ctx, now.Add(-denialRetention))
		_ = h.store.PurgeTasks(ctx, now.Add(-taskRetention))
		_ = h.store.PurgeLinkCodes(ctx, now)
		_ = h.store.PurgeInvites(ctx, now)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
