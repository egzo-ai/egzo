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
}

// New returns an engine with no projects.
func New() *Engine {
	return &Engine{projects: map[string]*engines.Project{}, events: map[string][]engines.Event{}, attached: map[string]int{}}
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
}

// RemoveProject makes a project disappear, as `project down` does.
func (e *Engine) RemoveProject(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.projects, name)
}

// SetAgent adds or replaces an agent of a project.
func (e *Engine) SetAgent(project string, a engines.Agent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.projects[project]
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

func (e *Engine) Project(ctx context.Context, name string) (engines.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
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

func (e *Engine) Spawn(ctx context.Context, project, template, name, actor string) (string, error) {
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
	p.Agents = append(p.Agents, engines.Agent{Name: name, Template: template, State: "running", Activity: "idle", Actor: actor, Created: time.Now()})
	return name, nil
}

func (e *Engine) Start(ctx context.Context, project, agent string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.agent(project, agent)
	if err != nil {
		return err
	}
	e.record("start %s/%s", project, agent)
	a.State, a.Activity = "running", "idle"
	return nil
}

func (e *Engine) Stop(ctx context.Context, project, agent string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.agent(project, agent)
	if err != nil {
		return err
	}
	e.record("stop %s/%s", project, agent)
	a.State, a.Activity = "exited", "stopped"
	return nil
}

func (e *Engine) Remove(ctx context.Context, project, agent string) error {
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
	return nil
}

// Attach is a tiny shell: it greets, echoes what is typed line by line, and ends on "exit". A read-only terminal only watches.
func (e *Engine) Attach(ctx context.Context, project, agent string, term engines.Terminal) error {
	e.mu.Lock()
	a, err := e.agent(project, agent)
	if err == nil && a.State != "running" {
		err = fmt.Errorf("agent %q is %s", agent, a.State)
	}
	if err == nil {
		e.record("attach %s/%s readonly=%v", project, agent, term.ReadOnly)
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
			if term.ReadOnly {
				continue
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
