// Package fake is an in-memory links.Conn: one linked project, the seam of the hub's tests and of the browser tests' server. What a real
// link pushes on its feeds it pushes too, and before the call that changed the project returns, so a test needs no waiting.
package fake

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// ErrDropped is what a verb fails with on a connection that has ended.
var ErrDropped = errors.New("session shutdown")

// Engine is a fake linked project, and its connection to the hub. Its fields are guarded by mu; use the methods to change it from a test.
type Engine struct {
	mu         sync.Mutex
	key        ed25519.PublicKey
	host       string
	project    links.Project
	published  *links.Published
	events     []links.Event
	calls      []string
	attached   map[string]int
	gate       chan struct{} // while not nil, a spawn, start, stop or removal waits for it to close: Hold, Release
	failNext   error         // the next spawn, start, stop or removal fails with it: FailNext
	projectN   int
	feedEvery  time.Duration
	feedSilent bool // it follows nothing: a control sidecar that logs no status
	audit      []links.AuditEvent
	auditSince []time.Time // the since of every connection that was served
	exposed    map[string][]links.Exposure
	servers    map[string]*server // what listens in an agent, by "agent:port"

	// the connection
	current  *Connection
	handlers *links.Handlers
	serving  bool
	sent     int // how far the audit trail was pushed
	pushed   bool
	verdicts []links.Verdict
}

// New returns a project of that name, on a machine called host, that publishes the templates, with a key of its own.
func New(name, host string, templates ...string) *Engine {
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	e := &Engine{key: public, host: host, attached: map[string]int{}, feedEvery: 30 * time.Second, exposed: map[string][]links.Exposure{}, servers: map[string]*server{},
		project: links.Project{Name: name, Up: true, Templates: templates, Services: []links.Service{{Name: "control", State: "running", ID: "control-1"}, {Name: "proxy", State: "running", ID: "proxy-1"}}}}
	e.current = &Connection{Engine: e, closed: make(chan struct{})}
	close(e.current.closed)
	return e
}

// Hello is what the link says first.
func (e *Engine) Hello() links.Hello {
	e.mu.Lock()
	defer e.mu.Unlock()
	return links.Hello{Protocol: hublink.Version, Project: e.project.Name, Host: e.host, Release: "dev", Key: e.key, Templates: append([]string(nil), e.project.Templates...)}
}

// Connection is one connection of the project to the hub: a links.Conn. The verbs are the project's, and work on the newest connection.
type Connection struct {
	*Engine
	closed chan struct{}
}

// Connect begins a new connection, which is the one that counts until the next.
func (e *Engine) Connect() *Connection {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.current = &Connection{Engine: e, closed: make(chan struct{})}
	e.handlers, e.serving, e.sent, e.pushed = nil, false, 0, false
	return e.current
}

// Drop ends the newest connection, as a link that went away does: whatever runs over it fails, and Run returns.
func (e *Engine) Drop() {
	e.mu.Lock()
	c := e.current
	e.mu.Unlock()
	c.Close()
}

// Closed is closed when the connection has ended.
func (c *Connection) Closed() <-chan struct{} { return c.closed }

// Close ends the connection.
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	if c.current == c {
		c.serving = false
	}
	return nil
}

// Finish ends the connection after its last verdict, which a link hangs up on.
func (c *Connection) Finish() error { return c.Close() }

// Closed is closed when the newest connection has ended.
func (e *Engine) Closed() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.current.closed
}

// Send takes a verdict as a link does: what an approval starts, anything else stops.
func (c *Connection) Send(v links.Verdict) error {
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return ErrDropped
	default:
	}
	c.verdicts = append(c.verdicts, v)
	serving := v.State == hublink.StateApproved
	begins := serving && !c.serving
	c.serving = serving
	c.mu.Unlock()
	if begins {
		c.startFeeds()
	}
	return nil
}

// Verdicts lists what the hub told the link.
func (e *Engine) Verdicts() []links.Verdict {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]links.Verdict(nil), e.verdicts...)
}

// Run waits for the end of the connection. The hub asks once it was told to serve: a real link pushes nothing before that.
func (c *Connection) Run(ctx context.Context, h links.Handlers) error {
	c.mu.Lock()
	c.handlers = &h
	serving := c.serving
	c.mu.Unlock()
	if serving {
		c.startFeeds()
	}
	select {
	case <-c.closed:
	case <-ctx.Done():
	}
	return ErrDropped
}

// Running says the hub took the connection: Run has begun.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.handlers != nil
}

// Served says the hub approved the connection, and heard the first push.
func (e *Engine) Served() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.serving && e.pushed
}

// Unlink is the link asking to be forgotten: it returns what the hub answers, which is the last thing on the connection.
func (e *Engine) Unlink() links.Verdict {
	e.mu.Lock()
	h, c := e.handlers, e.current
	e.mu.Unlock()
	v := h.Unlink()
	_ = c.Send(v)
	_ = c.Close()
	return v
}

// startFeeds pushes what a link pushes when its feeds open. A second call of a connection changes nothing: Send and Run both may come
// first.
func (e *Engine) startFeeds() {
	e.mu.Lock()
	h := e.handlers
	if h == nil || e.sent < 0 {
		e.mu.Unlock()
		return
	}
	e.sent = -1 // nothing is pushed until since is known
	e.mu.Unlock()
	since := h.Since()
	e.mu.Lock()
	e.auditSince = append(e.auditSince, since)
	e.sent = 0
	if since.IsZero() {
		e.sent = max(0, len(e.audit)-links.AuditBacklog)
	} else {
		for e.sent < len(e.audit) && !e.audit[e.sent].Time.After(since) {
			e.sent++
		}
	}
	e.mu.Unlock()
	e.push("start")
	e.mu.Lock()
	e.pushed = true
	e.mu.Unlock()
}

// AuditFollows lists the since of every connection, in order.
func (e *Engine) AuditFollows() []time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Time(nil), e.auditSince...)
}

// push sends what changed to the hub. It runs without the lock: what it calls is the hub's.
func (e *Engine) push(reason string) {
	e.mu.Lock()
	h, serving := e.handlers, e.serving
	if h == nil || !serving || e.sent < 0 {
		e.mu.Unlock()
		return
	}
	update := links.ProjectUpdate{Project: e.copyOf(), Published: e.publishedNow()}
	line, silent := e.statusLine(reason), e.feedSilent
	trail := append([]links.AuditEvent(nil), e.audit[e.sent:]...)
	e.sent = len(e.audit)
	e.mu.Unlock()
	h.Project(update)
	if !silent {
		h.Status(line)
	}
	for _, event := range trail {
		h.Audit(event)
	}
}

// pushAudit sends the end of the trail, and nothing else: an event of the proxy is no change of the project.
func (e *Engine) pushAudit() {
	e.mu.Lock()
	h, serving := e.handlers, e.serving
	if h == nil || !serving || e.sent < 0 {
		e.mu.Unlock()
		return
	}
	trail := append([]links.AuditEvent(nil), e.audit[e.sent:]...)
	e.sent = len(e.audit)
	e.mu.Unlock()
	for _, event := range trail {
		h.Audit(event)
	}
}

// Again is the same project on another machine's memory: the same key, for a second connection that has its own fate.
func (e *Engine) Again() *Engine {
	other := New(e.project.Name, e.host, e.project.Templates...)
	other.key = e.key
	return other
}

// Touch says nothing changed, as a heartbeat of the control sidecar does.
func (e *Engine) Touch() { e.push("heartbeat") }

// PushProject pushes an update that is not what the project holds: what a link that lies says.
func (e *Engine) PushProject(update links.ProjectUpdate) {
	e.mu.Lock()
	h := e.handlers
	e.mu.Unlock()
	h.Project(update)
}

// PushStatus pushes a status line that is not what the project holds.
func (e *Engine) PushStatus(line links.StatusLine) {
	e.mu.Lock()
	h := e.handlers
	e.mu.Unlock()
	h.Status(line)
}

func (e *Engine) copyOf() links.Project {
	c := e.project
	c.Agents = append([]links.Agent(nil), e.project.Agents...)
	c.Services = append([]links.Service(nil), e.project.Services...)
	c.Templates = append([]string(nil), e.project.Templates...)
	return c
}

// SetAgent adds or replaces an agent.
func (e *Engine) SetAgent(a links.Agent) {
	e.mu.Lock()
	replaced := false
	for i := range e.project.Agents {
		if e.project.Agents[i].Name == a.Name {
			e.project.Agents[i], replaced = a, true
		}
	}
	if !replaced {
		e.project.Agents = append(e.project.Agents, a)
	}
	e.mu.Unlock()
	e.push("change")
}

// SetServices replaces the sidecars, as one that stopped or came back does.
func (e *Engine) SetServices(services ...links.Service) {
	e.mu.Lock()
	e.project.Services = services
	e.mu.Unlock()
	e.push("change")
}

// AddEvent records an event of the project.
func (e *Engine) AddEvent(ev links.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

// Calls lists what was done to the project, as "verb agent actor".
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *Engine) record(format string, args ...any) {
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

// dropped says the connection has ended; it runs under the lock.
func (e *Engine) dropped() error {
	select {
	case <-e.current.closed:
		return ErrDropped
	default:
		return nil
	}
}

// ProjectReads says how many times the project was read.
func (e *Engine) ProjectReads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.projectN
}

// Project reads the project.
func (e *Engine) Project(ctx context.Context) (links.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.projectN++
	if err := e.dropped(); err != nil {
		return links.Project{}, err
	}
	return e.copyOf(), nil
}

// Agents is what the project holds now, whether or not a connection is up: for a test.
func (e *Engine) Agents() []links.Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]links.Agent(nil), e.project.Agents...)
}

func (e *Engine) agent(name string) (*links.Agent, error) {
	if err := e.dropped(); err != nil {
		return nil, err
	}
	for i := range e.project.Agents {
		if e.project.Agents[i].Name == name {
			return &e.project.Agents[i], nil
		}
	}
	return nil, fmt.Errorf("no agent %q in project %q", name, e.project.Name)
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

// wait waits while the project is held, then fails as FailNext said. A connection that ends ends it too.
func (e *Engine) wait(ctx context.Context) error {
	e.mu.Lock()
	gate, closed := e.gate, e.current.closed
	e.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-closed:
			return ErrDropped
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

// newInstanceID makes an id of the form instances have: core's is a ULID.
func newInstanceID() string {
	const letters = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw := make([]byte, 26)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i, b := range raw {
		raw[i] = letters[int(b)%len(letters)]
	}
	return string(raw)
}

// Spawn says it creates the network, waits while the project is held, then makes the agent.
func (e *Engine) Spawn(ctx context.Context, template, name, actor string, progress func(string)) (string, error) {
	project := e.project.Name
	progress(fmt.Sprintf("create network %s-%s", project, name))
	if err := e.wait(ctx); err != nil {
		return "", err
	}
	progress(fmt.Sprintf("create container %s-%s-1", project, name))
	e.mu.Lock()
	defer func() { e.push("change") }()
	defer e.mu.Unlock()
	if err := e.dropped(); err != nil {
		return "", err
	}
	found := false
	for _, t := range e.project.Templates {
		found = found || t == template
	}
	if !found {
		return "", fmt.Errorf("no template %q is published", template)
	}
	if name == "" {
		for n := 1; ; n++ {
			name = fmt.Sprintf("%s-%d", template, n)
			if _, err := e.agent(name); err != nil {
				break
			}
		}
	} else if _, err := e.agent(name); err == nil {
		return "", fmt.Errorf("instance %q already exists", name)
	}
	e.record("spawn %s %s", name, actor)
	e.project.Agents = append(e.project.Agents, links.Agent{Name: name, ID: newInstanceID(), Template: template, State: "running", Activity: "idle", Actor: actor,
		Created: time.Now()})
	return name, nil
}

// Start starts an agent.
func (e *Engine) Start(ctx context.Context, agent string) error {
	if err := e.wait(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	defer func() { e.push("change") }()
	defer e.mu.Unlock()
	a, err := e.agent(agent)
	if err != nil {
		return err
	}
	e.record("start %s", agent)
	a.State, a.Activity = "running", "idle"
	return nil
}

// Stop stops an agent.
func (e *Engine) Stop(ctx context.Context, agent string) error {
	if err := e.wait(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	defer func() { e.push("change") }()
	defer e.mu.Unlock()
	a, err := e.agent(agent)
	if err != nil {
		return err
	}
	e.record("stop %s", agent)
	a.State, a.Activity = "exited", "stopped"
	return nil
}

// Remove removes an agent.
func (e *Engine) Remove(ctx context.Context, agent string, progress func(string)) error {
	project := e.project.Name
	progress(fmt.Sprintf("remove container %s-%s-1", project, agent))
	if err := e.wait(ctx); err != nil {
		return err
	}
	progress(fmt.Sprintf("remove network %s-%s", project, agent))
	e.mu.Lock()
	defer func() { e.push("change") }()
	defer e.mu.Unlock()
	if _, err := e.agent(agent); err != nil {
		return err
	}
	e.record("rm %s", agent)
	for i := range e.project.Agents {
		if e.project.Agents[i].Name == agent {
			e.project.Agents = append(e.project.Agents[:i], e.project.Agents[i+1:]...)
			break
		}
	}
	return nil
}

// Attach is a tiny shell: it greets, echoes what is typed line by line, and ends on "exit".
func (e *Engine) Attach(ctx context.Context, agent string, term links.Terminal) error {
	e.mu.Lock()
	project := e.project.Name
	a, err := e.agent(agent)
	if err == nil && a.State != "running" {
		err = fmt.Errorf("agent %q is %s", agent, a.State)
	}
	if err == nil {
		e.record("attach %s", agent)
		e.attached[agent]++
	}
	closed := e.current.closed
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(term.In)
		line := ""
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			switch b {
			case '\r', '\n':
				if strings.TrimSpace(line) == "exit" {
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
	case <-done:
	case <-closed:
		return ErrDropped
	}
	return nil
}

// Expose has an agent expose a port, as the status feed says it. Without an agent's exposures: Unexpose.
func (e *Engine) Expose(agent string, exposure links.Exposure) {
	e.mu.Lock()
	e.exposed[agent] = append(e.exposed[agent], exposure)
	e.mu.Unlock()
	e.push("change")
}

// Unexpose takes a port back.
func (e *Engine) Unexpose(agent string, port int) {
	e.mu.Lock()
	kept := []links.Exposure{}
	for _, exposure := range e.exposed[agent] {
		if exposure.Port != port {
			kept = append(kept, exposure)
		}
	}
	e.exposed[agent] = kept
	e.mu.Unlock()
	e.push("change")
}

// Listen starts a server in an agent. Nothing else in the fake listens: a port with no server is a port where nothing listens.
func (e *Engine) Listen(agent string, port int, handler http.Handler) {
	s := &server{conns: make(chan net.Conn), done: make(chan struct{})}
	e.mu.Lock()
	e.servers[fmt.Sprintf("%s:%d", agent, port)] = s
	e.mu.Unlock()
	go (&http.Server{Handler: handler}).Serve(s)
}

// server takes the connections DialPort makes.
type server struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (s *server) Accept() (net.Conn, error) {
	select {
	case conn := <-s.conns:
		return conn, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *server) Close() error   { s.once.Do(func() { close(s.done) }); return nil }
func (s *server) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// DialPort reaches a server of Listen, through a pipe.
func (e *Engine) DialPort(ctx context.Context, agent string, port int) (net.Conn, error) {
	e.mu.Lock()
	a, err := e.agent(agent)
	if err == nil && a.State != "running" {
		err = fmt.Errorf("agent %q is %s", agent, a.State)
	}
	listening := e.servers[fmt.Sprintf("%s:%d", agent, port)]
	closed := e.current.closed
	if err == nil {
		e.record("dial %s %d", agent, port)
	}
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if listening == nil {
		return nil, fmt.Errorf("nothing listens on port %d", port)
	}
	near, far := net.Pipe()
	select {
	case listening.conns <- far:
		go func() { <-closed; near.Close() }()
		return near, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-closed:
		return nil, ErrDropped
	}
}

// Events lists the project's events.
func (e *Engine) Events(ctx context.Context, agent string, limit int) ([]links.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.dropped(); err != nil {
		return nil, err
	}
	var out []links.Event
	for _, ev := range e.events {
		if agent == "" || ev.Agent == agent {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Logs returns a log.
func (e *Engine) Logs(ctx context.Context, name string, tail int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.dropped(); err != nil {
		return "", err
	}
	return "log of " + name + "\nline 2\n", nil
}

// Spec returns the published templates.
func (e *Engine) Spec(ctx context.Context) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.dropped(); err != nil {
		return nil, err
	}
	return []byte("name: " + e.project.Name + "\nagents: [" + strings.Join(e.project.Templates, ", ") + "]\n"), nil
}

// SetFeedSilent makes the status feed say nothing, as a control sidecar that logs no status does.
func (e *Engine) SetFeedSilent(silent bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedSilent = silent
}

// SetFeedInterval sets the heartbeat a status line says it keeps.
func (e *Engine) SetFeedInterval(every time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedEvery = every
}

// SetPublished sets the templates the control sidecar holds, by name and hash.
func (e *Engine) SetPublished(p links.Published) {
	e.mu.Lock()
	e.published = &p
	e.mu.Unlock()
	e.push("change")
}

func (e *Engine) publishedNow() links.Published {
	if e.published != nil {
		return *e.published
	}
	out := links.Published{Templates: map[string]string{}}
	for _, t := range e.project.Templates {
		out.Templates[t] = "hash-" + t
	}
	return out
}

// Published reads the templates.
func (e *Engine) Published(ctx context.Context) (links.Published, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.dropped(); err != nil {
		return links.Published{}, err
	}
	return e.publishedNow(), nil
}

// statusLine is the state of the project, as a control sidecar would log it.
func (e *Engine) statusLine(reason string) links.StatusLine {
	line := links.StatusLine{Time: time.Now().UTC(), Reason: reason, Interval: e.feedEvery, Agents: []links.AgentStatus{}}
	for _, a := range e.project.Agents {
		line.Agents = append(line.Agents, links.AgentStatus{Agent: a.Name, Status: a.Status, Activity: a.Activity, Open: a.Open, Waiting: a.Waiting, Updated: line.Time, Exposed: e.exposed[a.Name]})
	}
	return line
}

// AddAudit appends events to the project's audit trail, as its proxy writes them; an event without a time gets the current one.
func (e *Engine) AddAudit(events ...links.AuditEvent) {
	e.mu.Lock()
	for _, event := range events {
		if event.Time.IsZero() {
			event.Time = time.Now().UTC()
		}
		e.audit = append(e.audit, event)
	}
	e.mu.Unlock()
	e.pushAudit()
}

var _ links.Conn = (*Connection)(nil)
