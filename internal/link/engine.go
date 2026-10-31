// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/stack"
)

// The types of the protocol, by the names this package has always used for them.
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
	Terminal     = hublink.Terminal
)

// Engine does what a hub asks of a project, on a Docker-compatible API, built on core's packages: what the CLI does, minus
// everything that reads a file of the host.
type Engine struct {
	client *engine.Client
}

// NewEngine wraps an open core engine client.
func NewEngine(c *engine.Client) *Engine { return &Engine{client: c} }

// Close closes the engine connection.
func (d *Engine) Close() error { return d.client.Close() }

// Client is the underlying connection, for the caller that opened it.
func (d *Engine) Client() *engine.Client { return d.client }

const (
	kindAgent   = "agent"
	kindControl = "control"
	kindProxy   = "proxy"
)

func (d *Engine) Projects(ctx context.Context) ([]Project, error) {
	list, err := d.client.API.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", engine.LabelProject))})
	if err != nil {
		return nil, err
	}
	byName := map[string]*Project{}
	project := func(name string) *Project {
		if byName[name] == nil {
			byName[name] = &Project{Name: name}
		}
		return byName[name]
	}
	for _, item := range list {
		id, err := engine.IdentityOf(item.Labels)
		if err != nil {
			// a project egzo could make is told; a container that names none is nobody's
			if name := item.Labels[engine.LabelProject]; names.Project(name) {
				p := project(name)
				p.Ignored = append(p.Ignored, fmt.Sprintf("container %s: %v", containerName(item), err))
			}
			continue
		}
		if id.Kind == "lock" {
			continue
		}
		p := project(id.Project)
		if p.Dir == "" {
			p.Dir, p.CreatedBy = id.ProjectDir, id.CreatedBy
		}
		switch id.Kind {
		case kindAgent:
			p.Agents = append(p.Agents, Agent{
				Name: id.Instance, ID: id.InstanceID, Template: id.Service, State: item.State,
				Actor: id.Actor, Created: time.Unix(item.Created, 0), Activity: activityOf(item.State),
				TemplateHash: id.TemplateHash,
			})
		case kindControl, kindProxy:
			p.Services = append(p.Services, Service{Name: id.Kind, State: item.State, Health: healthOf(item.Status), ID: item.ID})
		}
	}
	projects := make([]Project, 0, len(byName))
	for _, p := range byName {
		if len(p.Agents) == 0 && len(p.Services) == 0 {
			continue // only containers that are not egzo's claim it
		}
		p.Up = sidecarsRunning(p.Services)
		sort.Slice(p.Agents, func(i, j int) bool { return p.Agents[i].Name < p.Agents[j].Name })
		projects = append(projects, *p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func containerName(item container.Summary) string {
	if len(item.Names) > 0 {
		return strings.TrimPrefix(item.Names[0], "/")
	}
	return item.ID
}

func sidecarsRunning(services []Service) bool {
	running := map[string]bool{}
	for _, s := range services {
		running[s.Name] = s.State == "running"
	}
	return running[kindControl] && running[kindProxy]
}

// activityOf is what a container's state says of what its agent is doing: stopped when it is not running, and nothing more. A
// list of containers cannot say what a running agent is doing; the status feed does.
func activityOf(state string) string {
	if state != "running" {
		return "stopped"
	}
	return ""
}

// healthOf reads a container's health from the status text of a container listing ("Up 5 minutes (healthy)").
func healthOf(status string) string {
	switch {
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

// Project reads one project from the engine's containers and volumes. It runs no command in the control sidecar: the templates
// come from Published and what the agents are doing from FollowStatus.
func (d *Engine) Project(ctx context.Context, name string) (Project, error) {
	observed, err := stack.Observe(ctx, d.client, name)
	if err != nil {
		return Project{}, err
	}
	return projectOf(name, observed)
}

func projectOf(name string, observed stack.Observed) (Project, error) {
	project := Project{Name: name, Ignored: observed.Ignored}
	rows := stack.Rows(observed, nil, nil)
	hashes, ids := map[string]string{}, map[string]string{}
	for _, r := range observed.Resources {
		if r.Type != "container" {
			continue
		}
		if project.Dir == "" {
			project.Dir, project.CreatedBy = r.ProjectDir, ""
		}
		switch r.Kind {
		case kindAgent:
			hashes[r.Instance] = r.TemplateHash
		case kindControl, kindProxy:
			ids[r.Kind] = r.ID
		}
	}
	if len(rows) == 0 {
		return Project{}, ErrNoProject{Name: name}
	}
	for _, row := range rows {
		switch row.Kind {
		case kindAgent:
			project.Agents = append(project.Agents, Agent{
				Name: row.Name, ID: row.ID, Template: row.Service, State: row.State, Health: row.Health, Activity: activityOf(row.State),
				Actor: row.Actor, Created: row.Created, TemplateHash: hashes[row.Name],
			})
		case kindControl, kindProxy:
			project.Services = append(project.Services, Service{Name: row.Kind, State: row.State, Health: row.Health, ID: ids[row.Kind]})
		}
	}
	project.Up = sidecarsRunning(project.Services)
	sort.Slice(project.Agents, func(i, j int) bool { return project.Agents[i].Name < project.Agents[j].Name })
	return project, nil
}

// Published asks the control sidecar for the templates it holds, which runs a command in it.
func (d *Engine) Published(ctx context.Context, project string) (Published, error) {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return Published{}, err
	}
	out := Published{Templates: make(map[string]string, len(published.Templates))}
	for name, template := range published.Templates {
		out.Templates[name] = template.Hash
	}
	return out, nil
}

// instance finds an agent's container.
func (d *Engine) instance(ctx context.Context, project, agent string) (stack.Observed, *stack.Resource, error) {
	observed, err := stack.Observe(ctx, d.client, project)
	if err != nil {
		return observed, nil, err
	}
	instance, ok := observed.Instance(agent)
	if !ok {
		return observed, nil, fmt.Errorf("no agent %q in project %q", agent, project)
	}
	for i := range observed.Resources {
		r := &observed.Resources[i]
		if r.Type == "container" && r.Kind == kindAgent && r.Instance == instance.Name {
			return observed, r, nil
		}
	}
	return observed, nil, fmt.Errorf("agent %q has no container: it needs to be removed and spawned again", agent)
}

func (d *Engine) Spawn(ctx context.Context, project, template, name, actor string, progress func(string)) (string, error) {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return "", err
	}
	progress("take the project's lock")
	release, err := stack.LockWait(ctx, d.client, project, published.Image, 30*time.Second)
	if err != nil {
		return "", err
	}
	defer release()
	spawned, err := stack.Spawn(ctx, d.client, project, "", stack.SpawnRequest{Template: template, Name: name, Actor: actor}, &steps{progress: progress})
	if err != nil {
		return "", err
	}
	return spawned.Name, nil
}

func (d *Engine) Start(ctx context.Context, project, agent string) error {
	_, target, err := d.instance(ctx, project, agent)
	if err != nil {
		return err
	}
	if published, err := stack.ReadPublished(ctx, d.client, project); err == nil {
		view := published.AsProject(nil)
		if template, ok := view.Agents[target.Service]; ok {
			env := stack.PrepEnv{Image: published.Image, User: published.User}
			if err := stack.EnsureDirectoryVolumes(ctx, d.client, env, view, published.Dir, stack.DirectoryWorkspacesOf(view, template)...); err != nil {
				return err
			}
		}
	}
	return d.client.API.ContainerStart(ctx, target.ID, container.StartOptions{})
}

func (d *Engine) Stop(ctx context.Context, project, agent string) error {
	_, target, err := d.instance(ctx, project, agent)
	if err != nil {
		return err
	}
	timeout := 5
	return d.client.API.ContainerStop(ctx, target.ID, container.StopOptions{Timeout: &timeout})
}

func (d *Engine) Remove(ctx context.Context, project, agent string, progress func(string)) error {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return err
	}
	progress("take the project's lock")
	release, err := stack.LockWait(ctx, d.client, project, published.Image, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	observed, _, err := d.instance(ctx, project, agent)
	if err != nil {
		return err
	}
	env := stack.PrepEnv{Image: published.Image, User: published.User}
	_, err = stack.RemoveInstances(ctx, d.client, observed, env, project, published.Dir, []string{agent}, true, &steps{progress: progress})
	return err
}

// steps hands what core prints as it goes, a line per step, to progress.
type steps struct {
	mu       sync.Mutex
	progress func(string)
	partial  []byte
}

func (s *steps) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := strings.TrimSpace(string(s.partial[:i])); line != "" {
			s.progress(line)
		}
		s.partial = s.partial[i+1:]
	}
}

// Attachable says why an agent has no terminal to attach to, if it has none.
func (d *Engine) Attachable(ctx context.Context, project, agent string) error {
	_, err := d.runningInstance(ctx, project, agent)
	return err
}

func (d *Engine) runningInstance(ctx context.Context, project, agent string) (*stack.Resource, error) {
	_, target, err := d.instance(ctx, project, agent)
	if err != nil {
		return nil, err
	}
	if target.State != "running" {
		return nil, fmt.Errorf("agent %q is %s", agent, target.State)
	}
	return target, nil
}

// Attach runs `egzo agent attach` in the agent's container on a terminal, as `egzo attach` does.
func (d *Engine) Attach(ctx context.Context, project, agent string, term Terminal) error {
	target, err := d.runningInstance(ctx, project, agent)
	if err != nil {
		return err
	}
	command := []string{"egzo", "agent", "attach", "--detach-keys", "ctrl-]"}
	size := term.Size
	resized := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case s, ok := <-term.Resize:
				if !ok {
					return
				}
				size = s
				select {
				case resized <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	code, err := d.client.ExecStream(ctx, target.ID, command, engine.Stream{
		In: term.In, Out: term.Out, Err: term.Out, TTY: true,
		Size: func() (uint, uint) { return size.Cols, size.Rows },
	}, resized)
	if err != nil {
		return err
	}
	if code != 0 && code != 130 {
		return fmt.Errorf("attach ended with code %d", code)
	}
	return nil
}

// noisy are the event types that are not worth a person's time, or that carry message texts, which the hub does not show.
var noisy = map[string]bool{"state": true}

// withText are the types whose text is shown: what the agent says about itself. The text of a message never is.
var withText = map[string]bool{"status": true, "activity": true}

func (d *Engine) Events(ctx context.Context, project, agent string, limit int) ([]Event, error) {
	path := "/v1/events"
	if agent != "" {
		path += "?agent=" + url.QueryEscape(agent)
	}
	data, err := stack.ControlRequest(ctx, d.client, project, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	return ParseEvents(data, limit), nil
}

// ParseEvents reads the control sidecar's JSON lines: the last limit events that are worth showing, oldest first.
func ParseEvents(data []byte, limit int) []Event {
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		var raw struct {
			Seq   int       `json:"seq"`
			Time  time.Time `json:"time"`
			Type  string    `json:"type"`
			Agent string    `json:"agent"`
			Actor string    `json:"actor"`
			Text  string    `json:"text"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil || noisy[raw.Type] {
			continue
		}
		event := Event{Seq: raw.Seq, Time: raw.Time, Type: raw.Type, Agent: raw.Agent, Actor: raw.Actor}
		if withText[raw.Type] {
			event.Text = raw.Text
		}
		events = append(events, event)
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

func (d *Engine) Logs(ctx context.Context, project, name string, tail int) (string, error) {
	observed, err := stack.Observe(ctx, d.client, project)
	if err != nil {
		return "", err
	}
	var target *stack.Resource
	for i := range observed.Resources {
		r := &observed.Resources[i]
		if r.Type != "container" {
			continue
		}
		if r.Instance == name || r.Kind == name && r.Instance == "" {
			target = r
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no container %q in project %q", name, project)
	}
	reader, err := d.client.API.ContainerLogs(ctx, target.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: fmt.Sprint(tail)})
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var out bytes.Buffer
	if _, err := engine.Demux(&out, &out, io.LimitReader(reader, 4<<20)); err != nil && !errors.Is(err, io.EOF) {
		// a container with a terminal logs one raw stream
		return "", err
	}
	return tailThatFits(out.String()), nil
}

// tailThatFits keeps the end of a log, at a line's start, that is at most maxResult as JSON. A log can grow six times over, when it is
// all escape sequences.
func tailThatFits(text string) string {
	for {
		encoded, err := json.Marshal(text)
		if err != nil || len(encoded) <= maxResult {
			return text
		}
		text = text[len(text)-len(text)*maxResult/len(encoded)*9/10:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
}

// Spec returns the resolved snapshot `up` stored on the control volume: references to secrets, never their values.
func (d *Engine) Spec(ctx context.Context, project string) ([]byte, error) {
	list, err := stack.ControlRequest(ctx, d.client, project, "GET", "/v1/specs", nil)
	if err != nil {
		return nil, err
	}
	hashes := strings.Fields(string(list))
	if len(hashes) == 0 {
		return nil, errors.New("the project has stored no spec snapshot yet: run `egzo project up`")
	}
	// the current one is the last the CLI stored; the listing is sorted by hash, so ask for the one the labels agree with
	current := d.currentHash(ctx, project, hashes)
	return stack.ControlRequest(ctx, d.client, project, "GET", "/v1/specs/"+current, nil)
}

func (d *Engine) currentHash(ctx context.Context, project string, hashes []string) string {
	observed, err := stack.Observe(ctx, d.client, project)
	if err != nil {
		return hashes[len(hashes)-1]
	}
	control := ""
	for _, r := range observed.Resources {
		if r.Type == "container" && r.Kind == kindControl {
			control = r.ConfigHash
		}
	}
	for _, hash := range hashes {
		data, err := stack.ControlRequest(ctx, d.client, project, "GET", "/v1/specs/"+hash, nil)
		if err == nil && control != "" && bytes.Contains(data, []byte(control)) {
			return hash
		}
	}
	return hashes[len(hashes)-1]
}
