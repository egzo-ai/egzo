// Package fake is an in-memory engines.Engine: the engine seam of the hub's tests and of the browser tests' server.
package fake

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/internal/stack"
)

// Engine is a fake engine. Its fields are guarded by mu; use the methods to change it from a test.
type Engine struct {
	mu       sync.Mutex
	projects map[string]*engines.Project
	events   map[string][]engines.Event
	down     error
	calls    []string
	closed   bool
	attached map[string]int

	// the status feed and the event watch: what FollowStatus and Watch deliver
	feedWake    map[string]map[*chan struct{}]struct{} // by project: who is following
	feedStarts  map[string]int
	feedTries   map[string]int
	feedErr     map[string]error // by project: following fails with this
	feedSilent  map[string]bool  // by project: following works and says nothing, as a sidecar that logs no status does
	feedEvery   time.Duration
	watchWake   map[*chan struct{}]struct{}
	watchErr    error
	watchStarts int
	watchTries  int
	publishedBy map[string]engines.Published
	gate        chan struct{} // while not nil, a spawn, start, stop or removal waits for it to close: Hold, Release
	failNext    error         // the next spawn, start, stop or removal fails with it: FailNext
	publishedN  int
	projectN    int

	// the proxies' audit trails: what FollowAudit delivers
	audit      map[string][]engines.AuditEvent
	auditWake  map[string]map[*chan struct{}]struct{}
	auditSince map[string][]time.Time // by project: the since of every FollowAudit
}

// New returns an engine with no projects.
func New() *Engine {
	return &Engine{
		projects: map[string]*engines.Project{}, events: map[string][]engines.Event{}, attached: map[string]int{},
		feedWake: map[string]map[*chan struct{}]struct{}{}, feedStarts: map[string]int{}, feedTries: map[string]int{}, feedErr: map[string]error{}, feedSilent: map[string]bool{},
		feedEvery: 30 * time.Second, watchWake: map[*chan struct{}]struct{}{}, publishedBy: map[string]engines.Published{},
		audit: map[string][]engines.AuditEvent{}, auditWake: map[string]map[*chan struct{}]struct{}{}, auditSince: map[string][]time.Time{},
	}
}

// AddProject adds (or replaces) a project; Up is derived: a project is up when it has templates.
func (e *Engine) AddProject(p engines.Project) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(p.Services) == 0 {
		p.Services = []engines.Service{{Name: "control", State: "running"}, {Name: "proxy", State: "running"}}
	}
	p.Up = true
	e.projects[p.Name] = &p
	e.changed(p.Name)
}

// RemoveProject makes a project disappear, as `project down` does.
func (e *Engine) RemoveProject(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.projects, name)
	e.changed(name)
}

// SetAgent adds or replaces an agent of a project.
func (e *Engine) SetAgent(project string, a engines.Agent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.projects[project]
	defer e.changed(project)
	for i := range p.Agents {
		if p.Agents[i].Name == a.Name {
			p.Agents[i] = a
			return
		}
	}
	p.Agents = append(p.Agents, a)
}

// AddEvent records an event of a project.
func (e *Engine) AddEvent(project string, ev engines.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events[project] = append(e.events[project], ev)
}

// SetDown makes every call fail with err, as an unreachable engine does; nil brings it back.
func (e *Engine) SetDown(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.down = err
	e.changed("") // a watch of an engine that went down ends; the followers learn at their next line
	for _, followers := range e.auditWake {
		for wake := range followers {
			select {
			case *wake <- struct{}{}:
			default:
			}
		}
	}
}

// Calls lists what was done to the engine, as "verb project/agent actor".
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// Closed reports whether Close was called.
func (e *Engine) Closed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

func (e *Engine) record(format string, args ...any) {
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

func (e *Engine) copyOf(p *engines.Project) engines.Project {
	c := *p
	c.Agents = append([]engines.Agent(nil), p.Agents...)
	c.Services = append([]engines.Service(nil), p.Services...)
	c.Templates = append([]string(nil), p.Templates...)
	return c
}

func (e *Engine) Projects(ctx context.Context) ([]engines.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down != nil {
		return nil, e.down
	}
	var out []engines.Project
	for _, p := range e.projects {
		out = append(out, e.copyOf(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ProjectReads says how many times a project was read one at a time (a scan lists them all and does not count).
func (e *Engine) ProjectReads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.projectN
}

func (e *Engine) Project(ctx context.Context, name string) (engines.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.projectN++
	if e.down != nil {
		return engines.Project{}, e.down
	}
	p, ok := e.projects[name]
	if !ok {
		return engines.Project{}, engines.ErrNoProject{Name: name}
	}
	return e.copyOf(p), nil
}

func (e *Engine) agent(project, name string) (*engines.Agent, error) {
	if e.down != nil {
		return nil, e.down
	}
	p, ok := e.projects[project]
	if !ok {
		return nil, engines.ErrNoProject{Name: project}
	}
	for i := range p.Agents {
		if p.Agents[i].Name == name {
			return &p.Agents[i], nil
		}
	}
	return nil, fmt.Errorf("no agent %q in project %q", name, project)
}

// Hold makes every spawn, start, stop and removal wait, from now until Release, as one that takes long does.
func (e *Engine) Hold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gate == nil {
		e.gate = make(chan struct{})
	}
}

// Release lets what waits go on.
func (e *Engine) Release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gate != nil {
		close(e.gate)
		e.gate = nil
	}
}

// FailNext makes the next spawn, start, stop or removal fail with why, once it is no longer held.
func (e *Engine) FailNext(why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failNext = errors.New(why)
}

// wait waits while the engine is held, then fails as FailNext said.
func (e *Engine) wait(ctx context.Context) error {
	e.mu.Lock()
	gate := e.gate
	e.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.failNext
	e.failNext = nil
	return err
}

// Spawn says it creates the network, waits while the engine is held, then makes the agent.
func (e *Engine) Spawn(ctx context.Context, project, template, name, actor string, progress func(string)) (string, error) {
	progress(fmt.Sprintf("create network %s-%s", project, name))
	if err := e.wait(ctx); err != nil {
		return "", err
	}
	progress(fmt.Sprintf("create container %s-%s-1", project, name))
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down != nil {
		return "", e.down
	}
	p, ok := e.projects[project]
	if !ok {
		return "", engines.ErrNoProject{Name: project}
	}
	found := false
	for _, t := range p.Templates {
		found = found || t == template
	}
	if !found {
		return "", fmt.Errorf("no template %q is published", template)
	}
	if name == "" {
		for n := 1; ; n++ {
			name = fmt.Sprintf("%s-%d", template, n)
			if _, err := e.agent(project, name); err != nil {
				break
			}
		}
	} else if _, err := e.agent(project, name); err == nil {
		return "", fmt.Errorf("instance %q already exists", name)
	}
	e.record("spawn %s/%s %s", project, name, actor)
	defer e.changed(project)
	p.Agents = append(p.Agents, engines.Agent{Name: name, ID: stack.NewInstanceID(), Template: template, State: "running", Activity: "idle", Actor: actor,
		Created: time.Now()})
	return name, nil
}

func (e *Engine) Start(ctx context.Context, project, agent string) error {
	if err := e.wait(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.agent(project, agent)
	if err != nil {
		return err
	}
	e.record("start %s/%s", project, agent)
	a.State, a.Activity = "running", "idle"
	e.changed(project)
	return nil
}

func (e *Engine) Stop(ctx context.Context, project, agent string) error {
	if err := e.wait(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.agent(project, agent)
	if err != nil {
		return err
	}
	e.record("stop %s/%s", project, agent)
	a.State, a.Activity = "exited", "stopped"
	e.changed(project)
	return nil
}

func (e *Engine) Remove(ctx context.Context, project, agent string, progress func(string)) error {
	progress(fmt.Sprintf("remove container %s-%s-1", project, agent))
	if err := e.wait(ctx); err != nil {
		return err
	}
	progress(fmt.Sprintf("remove network %s-%s", project, agent))
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.agent(project, agent); err != nil {
		return err
	}
	e.record("rm %s/%s", project, agent)
	p := e.projects[project]
	for i := range p.Agents {
		if p.Agents[i].Name == agent {
			p.Agents = append(p.Agents[:i], p.Agents[i+1:]...)
			break
		}
	}
	e.changed(project)
	return nil
}

// Attach is a tiny shell: it greets, echoes what is typed line by line, and ends on "exit".
func (e *Engine) Attach(ctx context.Context, project, agent string, term engines.Terminal) error {
	e.mu.Lock()
	a, err := e.agent(project, agent)
	if err == nil && a.State != "running" {
		err = fmt.Errorf("agent %q is %s", agent, a.State)
	}
	if err == nil {
		e.record("attach %s/%s", project, agent)
		e.attached[project+"/"+agent]++
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	fmt.Fprintf(term.Out, "attached to %s/%s (%dx%d)\r\n$ ", project, agent, term.Size.Cols, term.Size.Rows)
	if term.Resize != nil {
		go func() {
			for s := range term.Resize {
				fmt.Fprintf(term.Out, "\r\n[resized to %dx%d]\r\n$ ", s.Cols, s.Rows)
			}
		}()
	}
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(term.In)
		line := ""
		for {
			b, err := reader.ReadByte()
			if err != nil {
				done <- nil
				return
			}
			switch b {
			case '\r', '\n':
				if strings.TrimSpace(line) == "exit" {
					done <- nil
					return
				}
				fmt.Fprintf(term.Out, "\r\nyou said: %s\r\n$ ", line)
				line = ""
			default:
				line += string(b)
				term.Out.Write([]byte{b})
			}
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-done:
		_ = err
	}
	return nil
}

func (e *Engine) Events(ctx context.Context, project, agent string, limit int) ([]engines.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down != nil {
		return nil, e.down
	}
	var out []engines.Event
	for _, ev := range e.events[project] {
		if agent == "" || ev.Agent == agent {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (e *Engine) Logs(ctx context.Context, project, name string, tail int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down != nil {
		return "", e.down
	}
	return "log of " + name + "\nline 2\n", nil
}

func (e *Engine) Spec(ctx context.Context, project string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down != nil {
		return nil, e.down
	}
	p, ok := e.projects[project]
	if !ok {
		return nil, engines.ErrNoProject{Name: project}
	}
	return []byte("name: " + project + "\nagents: [" + strings.Join(p.Templates, ", ") + "]\n"), nil
}

// --- the status feed, the event watch and the published templates ----------------------------------------------------

// changed tells the followers of a project and the watchers of the engine that something changed. It runs under the lock.
func (e *Engine) changed(project string) {
	for wake := range e.feedWake[project] {
		select {
		case *wake <- struct{}{}:
		default:
		}
	}
	for wake := range e.watchWake {
		select {
		case *wake <- struct{}{}:
		default:
		}
	}
}

// Touch makes the watchers call back, as an event of the engine does, without changing anything.
func (e *Engine) Touch() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.changed("")
}

// SetFeedError makes following a project's status fail with err, as an engine whose log cannot be read does; nil mends it.
func (e *Engine) SetFeedError(project string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedErr[project] = err
	e.changed(project)
}

// SetFeedSilent makes following a project's status work and deliver nothing, as a control sidecar that logs no status does.
func (e *Engine) SetFeedSilent(project string, silent bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedSilent[project] = silent
	e.changed(project)
}

// SetFeedInterval sets the heartbeat of the status lines the engine delivers.
func (e *Engine) SetFeedInterval(every time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedEvery = every
}

// FeedStarts says how many times a project's status was followed from the start.
func (e *Engine) FeedStarts(project string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.feedStarts[project]
}

// FeedTries says how many times following a project's status was attempted, whether it worked or not.
func (e *Engine) FeedTries(project string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.feedTries[project]
}

// Feeds says how many followers a project has right now.
func (e *Engine) Feeds(project string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.feedWake[project])
}

// SetWatchError makes watching the engine fail with err; nil mends it.
func (e *Engine) SetWatchError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.watchErr = err
}

// WatchTries says how many times watching the engine was attempted, whether it worked or not.
func (e *Engine) WatchTries() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watchTries
}

// WatchStarts says how many times the engine was watched.
func (e *Engine) WatchStarts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watchStarts
}

// SetPublished sets the templates the control sidecar of a project holds, by name and hash.
func (e *Engine) SetPublished(project string, p engines.Published) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishedBy[project] = p
}

// PublishedReads says how many times the published templates were asked for.
func (e *Engine) PublishedReads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.publishedN
}

func (e *Engine) Published(ctx context.Context, project string) (engines.Published, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishedN++
	if e.down != nil {
		return engines.Published{}, e.down
	}
	if p, ok := e.publishedBy[project]; ok {
		return p, nil
	}
	proj, ok := e.projects[project]
	if !ok {
		return engines.Published{}, engines.ErrNoProject{Name: project}
	}
	out := engines.Published{Templates: map[string]string{}}
	for _, t := range proj.Templates {
		out.Templates[t] = "hash-" + t
	}
	return out, nil
}

// statusLine is the state of the project, as a control sidecar would log it.
func (e *Engine) statusLine(project, reason string) (engines.StatusLine, bool) {
	p, ok := e.projects[project]
	if !ok {
		return engines.StatusLine{}, false
	}
	line := engines.StatusLine{Time: time.Now().UTC(), Reason: reason, Interval: e.feedEvery, Agents: []engines.AgentStatus{}}
	for _, a := range p.Agents {
		line.Agents = append(line.Agents, engines.AgentStatus{Agent: a.Name, Status: a.Status, Activity: a.Activity, Open: a.Open, Waiting: a.Waiting, Updated: line.Time})
	}
	return line, true
}

// FollowStatus delivers the state of the project at once, then again whenever it changes and every heartbeat.
func (e *Engine) FollowStatus(ctx context.Context, project string, on func(engines.StatusLine)) error {
	e.mu.Lock()
	e.feedTries[project]++
	if e.down != nil {
		err := e.down
		e.mu.Unlock()
		return err
	}
	if err := e.feedErr[project]; err != nil {
		e.mu.Unlock()
		return err
	}
	wake := make(chan struct{}, 1)
	if e.feedWake[project] == nil {
		e.feedWake[project] = map[*chan struct{}]struct{}{}
	}
	e.feedWake[project][&wake] = struct{}{}
	e.feedStarts[project]++
	every := e.feedEvery
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.feedWake[project], &wake)
		e.mu.Unlock()
	}()

	deliver := func(reason string) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.down != nil {
			return e.down
		}
		if err := e.feedErr[project]; err != nil {
			return err
		}
		if e.feedSilent[project] {
			return nil
		}
		if line, ok := e.statusLine(project, reason); ok {
			on(line)
		}
		return nil
	}
	if err := deliver("start"); err != nil {
		return err
	}
	beat := time.NewTicker(every)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-beat.C:
			if err := deliver("heartbeat"); err != nil {
				return err
			}
		case <-wake:
			if err := deliver("change"); err != nil {
				return err
			}
		}
	}
}

// Watch calls changed whenever the fake engine changes, or is touched.
func (e *Engine) Watch(ctx context.Context, changed func()) error {
	e.mu.Lock()
	e.watchTries++
	if e.watchErr != nil || e.down != nil {
		err := e.watchErr
		if err == nil {
			err = e.down
		}
		e.mu.Unlock()
		return err
	}
	wake := make(chan struct{}, 1)
	e.watchWake[&wake] = struct{}{}
	e.watchStarts++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.watchWake, &wake)
		e.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
			e.mu.Lock()
			err := e.down
			e.mu.Unlock()
			if err != nil {
				return err
			}
			changed()
		}
	}
}

// AddAudit appends events to a project's audit trail, as its proxy writes them; an event without a time gets the current one.
func (e *Engine) AddAudit(project string, events ...engines.AuditEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, event := range events {
		if event.Time.IsZero() {
			event.Time = time.Now().UTC()
		}
		e.audit[project] = append(e.audit[project], event)
	}
	for wake := range e.auditWake[project] {
		select {
		case *wake <- struct{}{}:
		default:
		}
	}
}

// AuditFollows lists the since of every FollowAudit of a project, in order.
func (e *Engine) AuditFollows(project string) []time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Time(nil), e.auditSince[project]...)
}

// FollowAudit delivers the trail after since (the last AuditBacklog events when since is zero), then each event as it is added.
func (e *Engine) FollowAudit(ctx context.Context, project string, since time.Time, on func(engines.AuditEvent)) error {
	e.mu.Lock()
	if e.down != nil {
		err := e.down
		e.mu.Unlock()
		return err
	}
	if _, ok := e.projects[project]; !ok {
		e.mu.Unlock()
		return engines.ErrNoProject{Name: project}
	}
	e.auditSince[project] = append(e.auditSince[project], since)
	wake := make(chan struct{}, 1)
	if e.auditWake[project] == nil {
		e.auditWake[project] = map[*chan struct{}]struct{}{}
	}
	e.auditWake[project][&wake] = struct{}{}
	trail := e.audit[project]
	next := 0
	if since.IsZero() {
		next = max(0, len(trail)-engines.AuditBacklog)
	} else {
		for next < len(trail) && !trail[next].Time.After(since) {
			next++
		}
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.auditWake[project], &wake)
		e.mu.Unlock()
	}()
	for {
		e.mu.Lock()
		if e.down != nil {
			err := e.down
			e.mu.Unlock()
			return err
		}
		pending := append([]engines.AuditEvent(nil), e.audit[project][next:]...)
		next = len(e.audit[project])
		e.mu.Unlock()
		for _, event := range pending {
			on(event)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		}
	}
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}

// ErrUnreachable is a convenient failure for SetDown.
var ErrUnreachable = errors.New("connection refused")

var _ engines.Engine = (*Engine)(nil)
var _ = io.EOF
