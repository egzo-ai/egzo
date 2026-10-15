package hub

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
)

// The hub learns what the agents are doing, and when containers change, from streams the engines push, never by asking on a timer:
//
//   - a status follower per project reads the control sidecar's standard output through the engine's log API (core writes a status
//     line on every change and a heartbeat every interval). It keeps the latest line, and is what Projects and Project show.
//   - an event watcher per engine reads the engine's event stream, and a change of containers makes the hub list that engine again.
//     The timed rescan that remains is a fallback for an event that was missed.
//
// Both streams are retried with a growing delay when they break. See roadmap/hub.md and known-issues/container-logging-follows-the-engine-default.md.

// FeedOptions tune the streams; the zero value is the production setting.
type FeedOptions struct {
	// Fallback is how often an engine is listed again whatever the events said, in case one was missed (60 seconds).
	Fallback time.Duration
	// Fast is how often an engine whose event stream is not working is listed (5 seconds): without events, listing is all there is.
	Fast time.Duration
	// BackoffMin and BackoffMax bound the delay before a broken stream is opened again (1 second, 1 minute).
	BackoffMin, BackoffMax time.Duration
	// Silent is how long a follower waits for the first status line before saying the sidecar writes none (90 seconds).
	Silent time.Duration
	// Debounce is how long events are gathered before the engine is listed again (250 milliseconds).
	Debounce time.Duration
	// Healthy is how long a stream must have run for its delay to start over (30 seconds).
	Healthy time.Duration
	// Grace is what is added to three heartbeats before a quiet sidecar is called stale (10 seconds); negative for none.
	Grace time.Duration
}

func (o FeedOptions) withDefaults() FeedOptions {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.Fallback, 60*time.Second)
	def(&o.Fast, 5*time.Second)
	def(&o.BackoffMin, time.Second)
	def(&o.BackoffMax, time.Minute)
	def(&o.Silent, 90*time.Second)
	def(&o.Debounce, 250*time.Millisecond)
	def(&o.Healthy, 30*time.Second)
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
	// FeedUnavailable: nothing can be read: the log cannot be followed, or the sidecar writes no status (it is older than this hub).
	FeedUnavailable FeedState = "unavailable"
)

// Feed is the state of a project's status, and why.
type Feed struct {
	State   FeedState
	Message string
}

// ActivityUnknown is what a running agent is doing when the feed cannot say.
const ActivityUnknown = "unknown"

type projectKey struct{ engine, project string }

// follower is one status follower and what it has heard.
type follower struct {
	cancel    context.CancelFunc
	controlID string // the sidecar container it follows: another one is another follower

	// what it heard, guarded by the manager's lock
	since     time.Time // when it was started, and the last time it connected
	connected bool
	err       string
	lastLine  time.Time // when the newest line was written, by the hub's clock
	interval  time.Duration
	agents    map[string]engines.AgentStatus
}

// engineWatch is one event watcher.
type engineWatch struct {
	cancel  context.CancelFunc
	healthy bool
}

// feedManager owns the followers and the watchers.
type feedManager struct {
	hub *Hub
	ctx context.Context
	opt FeedOptions

	mu        sync.Mutex
	followers map[projectKey]*follower
	watches   map[string]*engineWatch
	dirty     map[string]bool
	wake      chan struct{}
	scannedAt map[string]time.Time
}

func newFeedManager(h *Hub, ctx context.Context, opt FeedOptions) *feedManager {
	return &feedManager{hub: h, ctx: ctx, opt: opt.withDefaults(), followers: map[projectKey]*follower{}, watches: map[string]*engineWatch{},
		dirty: map[string]bool{}, wake: make(chan struct{}, 1), scannedAt: map[string]time.Time{}}
}

// StartFeeds turns on the streams: from now on every scan starts and stops the followers and watchers it needs. Run does this;
// tests that scan by hand call it, and before it the hub shows what the engines list.
func (h *Hub) StartFeeds(ctx context.Context, opt FeedOptions) {
	h.mu.Lock()
	h.feeds = newFeedManager(h, ctx, opt)
	h.mu.Unlock()
}

func (h *Hub) feedManager() *feedManager {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.feeds
}

// --- delay between attempts --------------------------------------------------------------------------------------------------

// backoff is the delay before a broken stream is opened again: it doubles up to a limit, with some jitter so that many streams that
// broke together do not come back together.
type backoff struct {
	min, max, next time.Duration
}

func newBackoff(min, max time.Duration) *backoff { return &backoff{min: min, max: max, next: min} }

func (b *backoff) reset() { b.next = b.min }

func (b *backoff) wait() time.Duration {
	d := b.next
	b.next *= 2
	if b.next > b.max {
		b.next = b.max
	}
	return d - d/5 + time.Duration(rand.Int64N(int64(d/5)*2+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// --- reconciling with what a scan found -----------------------------------------------------------------------------------------

// reconcile starts the followers and watchers the last scan calls for, and stops the rest. It runs after every scan.
func (m *feedManager) reconcile(scanned map[string]*engineScan, engineNames []string) {
	known := map[string]bool{}
	for _, name := range engineNames {
		known[name] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	for name := range known {
		if m.watches[name] == nil {
			m.startWatch(name)
		}
	}
	for name, w := range m.watches {
		if !known[name] {
			w.cancel()
			delete(m.watches, name)
			delete(m.scannedAt, name)
		}
	}

	wanted := map[projectKey]string{} // the projects whose control sidecar runs, and its container
	for name, scan := range scanned {
		if scan == nil || scan.err != nil {
			continue // an engine that does not answer is not a place to open streams
		}
		m.scannedAt[name] = scan.at
		for _, p := range scan.projects {
			for _, s := range p.Services {
				if s.Name == "control" && s.State == "running" {
					wanted[projectKey{name, p.Name}] = s.ID
				}
			}
		}
	}
	for key, f := range m.followers {
		if id, ok := wanted[key]; !ok || id != f.controlID {
			f.cancel()
			delete(m.followers, key)
		}
	}
	for key, id := range wanted {
		if m.followers[key] == nil {
			m.startFollower(key, id)
		}
	}
}

func (m *feedManager) startFollower(key projectKey, controlID string) {
	ctx, cancel := context.WithCancel(m.ctx)
	f := &follower{cancel: cancel, controlID: controlID, since: time.Now(), agents: map[string]engines.AgentStatus{}}
	m.followers[key] = f
	go m.follow(ctx, key, f)
}

func (m *feedManager) startWatch(name string) {
	ctx, cancel := context.WithCancel(m.ctx)
	w := &engineWatch{cancel: cancel}
	m.watches[name] = w
	go m.watch(ctx, name, w)
}

// --- the status follower ----------------------------------------------------------------------------------------------------------

func (m *feedManager) follow(ctx context.Context, key projectKey, f *follower) {
	delay := newBackoff(m.opt.BackoffMin, m.opt.BackoffMax)
	for ctx.Err() == nil {
		started := time.Now()
		m.mu.Lock()
		f.since, f.connected = started, true
		m.mu.Unlock()

		var err error
		eng, getErr := m.hub.registry.Get(ctx, key.engine)
		if getErr != nil {
			err = getErr
		} else {
			err = eng.FollowStatus(ctx, key.project, func(line engines.StatusLine) {
				delay.reset()
				m.apply(f, line, time.Since(started) < 2*time.Second)
			})
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = engines.ErrStreamEnded
		}
		if time.Since(started) >= m.opt.Healthy {
			delay.reset()
		}
		m.mu.Lock()
		f.connected, f.err = false, err.Error()
		m.mu.Unlock()
		m.hub.log.Debug("status stream ended", "engine", key.engine, "project", key.project, "err", err)
		if !sleep(ctx, delay.wait()) {
			return
		}
	}
}

// apply keeps what a status line says. A line that came with the log's replay at the start is as old as it was written, and
// it is not allowed to make a silent sidecar look alive.
func (m *feedManager) apply(f *follower, line engines.StatusLine, replay bool) {
	now := time.Now()
	age := time.Duration(0)
	if replay && !line.Time.IsZero() && now.After(line.Time) {
		age = now.Sub(line.Time)
	}
	agents := make(map[string]engines.AgentStatus, len(line.Agents))
	for _, a := range line.Agents {
		agents[a.Agent] = a
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f.err = ""
	f.lastLine = now.Add(-age)
	f.interval = line.Interval
	f.agents = agents
}

// state says how far a follower can be trusted at a moment.
func (m *feedManager) state(f *follower, now time.Time) Feed {
	if f.lastLine.IsZero() {
		switch {
		case f.err != "":
			return Feed{FeedUnavailable, "the status log of the control sidecar cannot be followed: " + f.err}
		case now.Sub(f.since) > m.opt.Silent:
			return Feed{FeedUnavailable, "the control sidecar writes no status: it is older than this hub (run egzo project up again), or its log cannot be read"}
		}
		return Feed{FeedConnecting, "waiting for the control sidecar's first status line"}
	}
	interval := f.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if age := now.Sub(f.lastLine); age > 3*interval+m.opt.Grace {
		msg := fmt.Sprintf("no status from the control sidecar for %s", age.Round(time.Second))
		if f.err != "" {
			msg += ": " + f.err
		}
		return Feed{FeedStale, msg}
	}
	return Feed{FeedLive, ""}
}

// overlay returns the agents of a project with what the feed says they are doing, and the state of the feed. It never changes
// what it is given. Without feeds started the agents are returned as they are.
func (h *Hub) overlay(engine string, p engines.Project) (engines.Project, Feed) {
	m := h.feedManager()
	if m == nil {
		return p, Feed{State: FeedLive}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.followers[projectKey{engine, p.Name}]
	feed := Feed{FeedUnavailable, "the project's control sidecar is not running"}
	if f != nil {
		feed = m.state(f, time.Now())
	}
	agents := make([]engines.Agent, len(p.Agents))
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

// --- the event watcher --------------------------------------------------------------------------------------------------------------

func (m *feedManager) watch(ctx context.Context, name string, w *engineWatch) {
	delay := newBackoff(m.opt.BackoffMin, m.opt.BackoffMax)
	for ctx.Err() == nil {
		started := time.Now()
		var err error
		eng, getErr := m.hub.registry.Get(ctx, name)
		if getErr != nil {
			err = getErr
		} else {
			m.setHealthy(w, true)
			// events may have been missed while the stream was down
			m.markDirty(name)
			err = eng.Watch(ctx, func() { delay.reset(); m.markDirty(name) })
		}
		m.setHealthy(w, false)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = engines.ErrStreamEnded
		}
		if time.Since(started) >= m.opt.Healthy {
			delay.reset()
		}
		m.hub.log.Debug("event stream ended", "engine", name, "err", err)
		if !sleep(ctx, delay.wait()) {
			return
		}
	}
}

func (m *feedManager) setHealthy(w *engineWatch, healthy bool) {
	m.mu.Lock()
	w.healthy = healthy
	m.mu.Unlock()
}

// markDirty says an engine changed and has to be listed again.
func (m *feedManager) markDirty(engine string) {
	m.mu.Lock()
	m.dirty[engine] = true
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// takeDirty returns the engines to list again for what the events said.
func (m *feedManager) takeDirty() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name := range m.dirty {
		names = append(names, name)
	}
	m.dirty = map[string]bool{}
	return names
}

// due returns the engines that must be listed now whatever the events said: one that was not listed for a Fallback, and one whose
// events are not arriving, which is listed on a short timer because that is all there is.
func (m *feedManager) due(now time.Time) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name, w := range m.watches {
		last := m.scannedAt[name]
		switch {
		case last.IsZero(), now.Sub(last) >= m.opt.Fallback, !w.healthy && now.Sub(last) >= m.opt.Fast:
			names = append(names, name)
		}
	}
	return names
}

// Run is discovery and the streams: it lists every engine, then lists one again when its events say something changed, or when it
// has not been listed for a while, and keeps the status followers and event watchers in step with what it finds. It returns when
// ctx ends.
func (h *Hub) Run(ctx context.Context, opt FeedOptions) {
	h.StartFeeds(ctx, opt)
	m := h.feedManager()
	scan := func(names []string) {
		listing, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var err error
		if names == nil {
			err = h.Scan(listing)
		} else {
			err = h.scanEngines(listing, names)
		}
		if err != nil && ctx.Err() == nil {
			h.log.Warn("scan failed", "err", err)
		}
		_ = h.store.PurgeSessions(listing)
	}
	scan(nil)
	tick := time.NewTicker(m.opt.Fast)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			// let the events of one change come in together
			if !sleep(ctx, m.opt.Debounce) {
				return
			}
			if names := m.takeDirty(); len(names) > 0 {
				scan(names)
			}
		case now := <-tick.C:
			if names := m.due(now); len(names) > 0 {
				scan(names)
			}
		}
	}
}
