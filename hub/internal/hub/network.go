package hub

import (
	"context"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// What the agents reached, and what they were refused, comes from the proxy's audit trail, which the project's link follows and pushes.
// An audit follower per project, which outlives its connections, takes it and:
//
//   - keeps the latest events in memory, for the Log of the Network view;
//   - groups the refusals and the failures by instance, protocol, host and reason code, and writes the groups to the database, so
//     that they outlive the proxy's container and the engine's log retention. The events themselves are not kept: long-term audit
//     is the log pipeline of the engine's sysadmin;
//   - marks a group fixed when the same instance gets through to the host afterwards.
//
// A link that connects says from when it is wanted: the time of the last event handled. See roadmap/proxy-audit.md.

const (
	// recentEvents is how many events of a project the hub keeps in memory.
	recentEvents = 1000
	// denialRetention is how long a group is kept after it was last seen.
	denialRetention = 30 * 24 * time.Hour
	// deniedWindow is how far back the count of denied hosts on the agents list looks.
	deniedWindow = time.Hour
)

// fixers says which allowed steps show that a refused step works now: a connect for a connect, a request for a request or a
// failed handshake, a command, shell or sftp session for an ssh server that could not be reached.
var fixers = map[proxy.Kind][]proxy.Kind{
	proxy.KindConnect:   {proxy.KindConnect},
	proxy.KindRequest:   {proxy.KindRequest},
	proxy.KindHandshake: {proxy.KindRequest},
	proxy.KindUpstream:  {proxy.KindExec, proxy.KindShell, proxy.KindSFTP},
}

// isDenial says whether an event is grouped: a refusal or a failure, except a client that left before its handshake (clients open
// connections ahead of time and drop them).
func isDenial(e links.AuditEvent) bool {
	if e.Decision != proxy.OutcomeDeny && e.Decision != proxy.OutcomeError {
		return false
	}
	return e.Reason == nil || e.Reason.Code != proxy.CodeAborted
}

// groupKey is a group: the claimed name is part of it, so attempts with wrong credentials are told apart by the name they used (an
// authenticated one always claims its own).
type groupKey struct{ instance, claimed, protocol, host, code string }

// hostKey is where a group was refused: what an allowed event is compared with to see a fix.
type hostKey struct{ instance, protocol, host string }

func (k groupKey) where() hostKey { return hostKey{k.instance, k.protocol, k.host} }

// maxOpen bounds the open groups a follower keeps in memory, as the database bounds the groups it keeps.
const maxOpen = 2 * store.MaxDenials

// openGroup is a group that is not fixed, as the follower keeps it to see a fix and to count denied hosts.
type openGroup struct {
	event proxy.Kind
	last  time.Time
}

// auditFollower is one audit follower and what it has heard.
type auditFollower struct {
	flushMu sync.Mutex // one flush at a time, so that the batches are written in the order they were heard

	// guarded by the manager's lock
	connected bool
	cursor    time.Time       // the time of the last event handled
	atCursor  map[string]bool // the events handled at that very time, by connection and step: a stream opened again replays them
	resumed   bool            // the cursor is the database\'s: every event at it was handled by an earlier run
	events    []links.AuditEvent
	open      map[hostKey]map[string]openGroup // by where, then by reason code
	openCount int                              // groups in open, give or take the fixed ones: recounted when it passes maxOpen
	pending   map[groupKey]*store.Denial
	fixes     []fix
	dirty     bool // the cursor moved since it was written
	// ignored counts the lines that parse as events but are not events the proxy writes (proxy.Event.Check), and lastIgnored
	// says why the last one was not
	ignored     int
	lastIgnored string
}

type fix struct {
	key    groupKey
	events []string
	at     time.Time
}

// startAudit readies the follower of a project for a connection: what the database holds, the first time, and the flushing of what it
// hears until the connection ends.
func (m *feedManager) startAudit(c *connection) {
	key := c.project
	m.mu.Lock()
	f := m.audits[key]
	fresh := f == nil
	if fresh {
		f = &auditFollower{open: map[hostKey]map[string]openGroup{}, pending: map[groupKey]*store.Denial{}, atCursor: map[string]bool{}}
		m.audits[key] = f
	}
	f.connected = true
	m.mu.Unlock()
	if fresh {
		m.loadAudit(c.ctx, key, f)
	}
	go func() {
		tick := time.NewTicker(m.opt.Flush)
		defer tick.Stop()
		for {
			select {
			case <-c.ctx.Done():
				m.flushAudit(context.WithoutCancel(c.ctx), key, f)
				return
			case <-tick.C:
				m.flushAudit(c.ctx, key, f)
			}
		}
	}()
}

// loadAudit takes from the database the groups that are open, and how far the trail was read.
func (m *feedManager) loadAudit(ctx context.Context, key string, f *auditFollower) {
	store := m.hub.store
	cursor, err := store.AuditCursor(ctx, key)
	if err != nil && ctx.Err() == nil {
		m.hub.log.Warn("read the audit cursor", "project", key, "err", err)
	}
	if oldest := time.Now().Add(-denialRetention); !cursor.IsZero() && cursor.Before(oldest) {
		cursor = oldest
	}
	groups, _ := store.Denials(ctx, key)
	m.mu.Lock()
	defer m.mu.Unlock()
	f.cursor, f.resumed = cursor, !cursor.IsZero()
	for _, d := range groups {
		if d.Fixed.IsZero() {
			f.opened(groupKey{d.Instance, d.ClaimedAgent, d.Protocol, d.Host, d.Code}, openGroup{event: proxy.Kind(d.Event), last: d.Last})
		}
	}
}

// since says from when a link is to send the trail.
func (m *feedManager) since(key string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.audits[key]; f != nil {
		return f.cursor
	}
	return time.Time{}
}

// handleAudit takes one event of the trail.
func (m *feedManager) handleAudit(key string, e links.AuditEvent) {
	m.mu.Lock()
	f := m.audits[key]
	m.mu.Unlock()
	if f == nil {
		return
	}
	if err := e.Check(); err != nil {
		// not an event the proxy writes: a link says what it wants
		m.mu.Lock()
		f.ignored, f.lastIgnored = f.ignored+1, err.Error()
		m.mu.Unlock()
		return
	}
	denial := isDenial(e)
	var actor string
	if denial && e.Instance != "" {
		actor = m.hub.actorOf(key, e.Instance) // before the lock: it takes the hub's
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := e.Conn + " " + string(e.Kind)
	switch {
	case e.Time.Before(f.cursor), e.Time.Equal(f.cursor) && (f.atCursor[id] || f.resumed):
		return // read again after the stream was opened again
	case e.Time.After(f.cursor):
		f.cursor, f.atCursor, f.resumed = e.Time, map[string]bool{}, false
	}
	f.atCursor[id], f.dirty = true, true
	f.events = append(f.events, e)
	if len(f.events) > recentEvents {
		f.events = append([]links.AuditEvent(nil), f.events[len(f.events)-recentEvents:]...)
	}

	if denial {
		code, text := "", ""
		if e.Reason != nil {
			code, text = string(e.Reason.Code), e.Reason.Text
		}
		k := groupKey{e.Instance, e.ClaimedAgent, string(e.Protocol), e.Host, code}
		d := f.pending[k]
		if d == nil {
			d = &store.Denial{Project: key, Instance: e.Instance, ClaimedAgent: e.ClaimedAgent, Protocol: string(e.Protocol),
				Host: e.Host, Code: code, First: e.Time}
			f.pending[k] = d
		}
		d.Event, d.Decision, d.Agent, d.Profile, d.Server, d.Text, d.Last = string(e.Kind), string(e.Decision), e.Agent, e.Profile, e.Server, text, e.Time
		d.Client, d.UserAgent = e.Client, e.UserAgent
		if actor != "" {
			d.Actor = actor
		}
		d.Count++
		f.opened(k, openGroup{event: e.Kind, last: e.Time})
		return
	}
	if e.Decision != proxy.OutcomeAllow || e.Instance == "" {
		return
	}
	where := hostKey{e.Instance, string(e.Protocol), e.Host}
	for code, g := range f.open[where] {
		if g.last.After(e.Time) {
			continue
		}
		for _, by := range fixers[g.event] {
			if by == e.Kind {
				delete(f.open[where], code)
				f.fixes = append(f.fixes, fix{key: groupKey{instance: where.instance, protocol: where.protocol, host: where.host, code: code}, events: []string{string(g.event)}, at: e.Time})
				break
			}
		}
	}
	if len(f.open[where]) == 0 {
		delete(f.open, where)
	}
}

// opened keeps a group as open. Past maxOpen, the group seen least recently is forgotten: it stays in the database, and is only not
// marked fixed nor counted on the agents list.
func (f *auditFollower) opened(k groupKey, g openGroup) {
	where := k.where()
	if f.open[where] == nil {
		f.open[where] = map[string]openGroup{}
	}
	f.open[where][k.code] = g
	if f.openCount++; f.openCount <= maxOpen {
		return
	}
	n, oldest, oldestCode := 0, hostKey{}, ""
	var at time.Time
	for w, codes := range f.open {
		for code, g := range codes {
			n++
			if at.IsZero() || g.last.Before(at) {
				oldest, oldestCode, at = w, code, g.last
			}
		}
	}
	f.openCount = n
	if n > maxOpen {
		delete(f.open[oldest], oldestCode)
		if len(f.open[oldest]) == 0 {
			delete(f.open, oldest)
		}
		f.openCount--
	}
}

// flushAudit writes what was heard since the last flush: the groups, then the fixes, then how far the trail was read.
func (m *feedManager) flushAudit(ctx context.Context, key string, f *auditFollower) {
	f.flushMu.Lock()
	defer f.flushMu.Unlock()
	m.mu.Lock()
	pending, fixes, cursor, dirty := f.pending, f.fixes, f.cursor, f.dirty
	f.pending, f.fixes, f.dirty = map[groupKey]*store.Denial{}, nil, false
	m.mu.Unlock()
	if len(pending) == 0 && len(fixes) == 0 && !dirty {
		return
	}
	s := m.hub.store
	seen := make([]store.Denial, 0, len(pending))
	for _, d := range pending {
		seen = append(seen, *d)
	}
	err := s.RecordDenials(ctx, seen)
	for _, x := range fixes {
		if err == nil {
			err = s.FixDenials(ctx, key, x.key.instance, x.key.protocol, x.key.host, x.events, x.at)
		}
	}
	if err == nil && dirty {
		err = s.SetAuditCursor(ctx, key, cursor)
	}
	if err != nil {
		m.hub.log.Warn("write the denials", "project", key, "err", err)
	}
}

// actorOf finds who spawned an instance, by its id, in what the link last pushed.
func (h *Hub) actorOf(project, instance string) string {
	p, ok := h.knownProject(project)
	if !ok {
		return ""
	}
	for _, a := range p.Agents {
		if a.ID == instance {
			return a.Actor
		}
	}
	return ""
}

// auditFeed says how far the network view of a project can be trusted.
func (m *feedManager) auditFeed(f *auditFollower, proxyRunning bool) Feed {
	switch {
	case f == nil || !f.connected:
		return Feed{FeedUnavailable, "the project is offline: this is what the hub last heard"}
	case !proxyRunning:
		return Feed{FeedUnavailable, "the project's proxy is not running"}
	}
	return Feed{FeedLive, ""}
}

// deniedCounts counts, for each instance of a project, the hosts it was refused in the last deniedWindow that are not fixed.
func (h *Hub) deniedCounts(project string) map[string]int {
	m := h.feeds
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.audits[project]
	if f == nil {
		return nil
	}
	since := time.Now().Add(-deniedWindow)
	hosts := map[string]map[string]bool{}
	for where, codes := range f.open {
		for _, g := range codes {
			if where.instance == "" || g.last.Before(since) {
				continue
			}
			if hosts[where.instance] == nil {
				hosts[where.instance] = map[string]bool{}
			}
			hosts[where.instance][where.host] = true
		}
	}
	counts := make(map[string]int, len(hosts))
	for instance, set := range hosts {
		counts[instance] = len(set)
	}
	return counts
}

// NetworkView is what the proxy of a project recorded: the groups of what it denied, and its latest events, oldest first.
type NetworkView struct {
	Feed    Feed
	Denials []store.Denial
	Events  []links.AuditEvent
	// Ignored is how many events a link sent the hub refused as events the proxy never writes, and IgnoredWhy why the last
	// one was; an administrator's view only.
	Ignored    int
	IgnoredWhy string
}

// Network reads what a project's proxy recorded, or what it recorded for one agent (the instance of that name that exists now).
// An administrator sees every agent; a user sees the agents they may operate, as for their logs.
func (h *Hub) Network(ctx context.Context, user store.User, project, agent string) (NetworkView, error) {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return NetworkView{}, err
	}
	known, _ := h.knownProject(project)
	actors := map[string]string{} // instance id -> who spawned it
	only := ""
	proxyRunning := false
	for _, s := range known.Services {
		proxyRunning = proxyRunning || s.Name == "proxy" && s.State == "running"
	}
	for _, a := range known.Agents {
		if a.ID != "" {
			actors[a.ID] = a.Actor
		}
		if a.Name == agent {
			only = a.ID
		}
	}
	if agent != "" && only == "" {
		return NetworkView{}, ErrNotFound
	}
	// what the follower heard and has not written yet is written first: the view is never behind the agents list
	m := h.feeds
	m.mu.Lock()
	f := m.audits[project]
	m.mu.Unlock()
	if f != nil {
		m.flushAudit(context.WithoutCancel(ctx), project, f) // a batch is not lost to a request that went away
	}
	denials, err := h.store.Denials(ctx, project)
	if err != nil {
		return NetworkView{}, err
	}
	for _, d := range denials {
		if _, known := actors[d.Instance]; !known && d.Actor != "" {
			actors[d.Instance] = d.Actor // a removed instance
		}
	}
	everyone := role.AtLeast(rbac.Administrator)
	visible := func(instance string) bool {
		if only != "" {
			return instance == only && (everyone || rbac.Can(user.Subject(), role, rbac.Operate, actors[instance]))
		}
		if everyone {
			return true
		}
		actor, ok := actors[instance]
		return ok && instance != "" && rbac.Can(user.Subject(), role, rbac.Operate, actor)
	}

	view := NetworkView{Denials: []store.Denial{}, Events: []links.AuditEvent{}}
	for _, d := range denials {
		if visible(d.Instance) {
			view.Denials = append(view.Denials, d)
		}
	}
	m.mu.Lock()
	view.Feed = m.auditFeed(f, proxyRunning)
	var events []links.AuditEvent
	if f != nil {
		events = append(events, f.events...)
		if everyone {
			view.Ignored, view.IgnoredWhy = f.ignored, f.lastIgnored
		}
	}
	m.mu.Unlock()
	for _, e := range events {
		if visible(e.Instance) {
			view.Events = append(view.Events, e)
		}
	}
	return view, nil
}
