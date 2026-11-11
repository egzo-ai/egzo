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
	"github.com/docker/docker/api/types/volume"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/files"
	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/portforward"
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
	_, err = stack.RemoveInstances(ctx, d.client, observed, env, project, published.Dir, []string{agent}, true, false, &steps{progress: progress})
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

// Forward runs `egzo agent forward` in the agent's container for one connection. It finds the container with one question to the engine: it
// runs for every connection a browser opens, so stack.Observe, a round trip per container, is too much.
func (d *Engine) Forward(ctx context.Context, project, agent string, port int, closeOnEOF bool, in io.Reader, out, said io.Writer) error {
	list, err := d.client.API.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"="+kindAgent),
		filters.Arg("label", engine.LabelInstance+"="+agent), filters.Arg("status", "running"))})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("agent %q is not running", agent)
	}
	code, err := d.client.ExecStream(ctx, list[0].ID, portforward.AnnouncedCommand(port, closeOnEOF), engine.Stream{In: in, Out: out, Err: said}, nil)
	if err == nil && code != 0 {
		return fmt.Errorf("the forward ended with code %d", code)
	}
	return err
}

// noisy are the event types that are not worth a person's time.
var noisy = map[string]bool{"state": true}

// withText are the types whose text is shown: what the agent says about itself. The text of a message is only in the events feed,
// and only when a user is one end of it.
var withText = map[string]bool{"status": true, "activity": true, "exposed": true, "unexposed": true}

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

// ParseEvents reads the control sidecar's JSON lines: the last limit events that are worth showing, oldest first. No message
// text is in them: they are what the web UI shows.
func ParseEvents(data []byte, limit int) []Event {
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		if event, _, ok := parseEvent(scanner.Bytes(), false); ok {
			events = append(events, event)
		}
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

// parseEvent reads one line of the control sidecar's event stream. seq is the line's sequence number, also for an event that is not
// worth showing (ok false). userTexts keeps the text of a message one end of which is a user: what the hub relays to its users.
func parseEvent(line []byte, userTexts bool) (event Event, seq int, ok bool) {
	var raw struct {
		Seq   int       `json:"seq"`
		Time  time.Time `json:"time"`
		Type  string    `json:"type"`
		Agent string    `json:"agent"`
		Actor string    `json:"actor"`
		ID    string    `json:"id"`
		Text  string    `json:"text"`
		Data  struct {
			To      string   `json:"to"`
			Kind    string   `json:"kind"`
			Re      string   `json:"re"`
			Outcome string   `json:"outcome"`
			Choices []string `json:"choices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		var bare struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(line, &bare)
		return Event{}, bare.Seq, false
	}
	if noisy[raw.Type] {
		return Event{}, raw.Seq, false
	}
	event = Event{Seq: raw.Seq, Time: raw.Time, Type: raw.Type, Agent: raw.Agent, Actor: raw.Actor, ID: raw.ID, Outcome: raw.Data.Outcome}
	if raw.Type == "message" {
		event.To, event.Kind, event.Re = raw.Data.To, raw.Data.Kind, raw.Data.Re
	}
	switch {
	case withText[raw.Type]:
		event.Text = raw.Text
	case raw.Type == "message" && userTexts && (isUser(raw.Actor) || isUser(raw.Data.To)):
		// the choices are words of the sender too: they go with the text
		event.Text, event.Choices = raw.Text, raw.Data.Choices
	}
	return event, raw.Seq, true
}

func isUser(address string) bool { return strings.HasPrefix(address, "user:") }

// FollowEvents follows the control sidecar's event stream from the events after seq, or from the next one when seq is negative, with
// the texts of the messages users send and are sent. on gets each event worth showing; seen, every sequence number read, so that
// following again starts past those too. From the next one, the first event is Type "start", whose Seq says where that is (0 for an
// empty log): following again from there misses nothing that came in between.
func (d *Engine) FollowEvents(ctx context.Context, project string, after int, on func(Event), seen func(int)) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := stack.ControlStream(ctx, d.client, project, "GET", fmt.Sprintf("/v1/events?after=%d&follow=1", after), writer, io.Discard)
		writer.CloseWithError(err)
		done <- err
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxLogLine)
	for scanner.Scan() {
		var start struct {
			Type string `json:"type"`
			Seq  int    `json:"seq"`
		}
		if json.Unmarshal(scanner.Bytes(), &start) == nil && start.Type == "start" {
			on(Event{Type: "start", Seq: start.Seq})
			seen(start.Seq)
			continue
		}
		event, seq, ok := parseEvent(scanner.Bytes(), true)
		if ok {
			on(event)
		}
		if seq > 0 {
			seen(seq)
		}
	}
	reader.CloseWithError(io.EOF) // the stream may still be writing a line too long to read: let it end
	err := <-done
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = scanner.Err()
	}
	if err == nil {
		return hublink.ErrStreamEnded
	}
	return err
}

// Send is a message from one of the hub's users to an agent. The hub speaks for its users and no one else, and every message it
// sends has a key, the chat platform's id for it, so that a retry is the same message.
func (d *Engine) Send(ctx context.Context, project, agent, actor, text, key string, interrupt bool) (hublink.Sent, error) {
	if !isUser(actor) {
		return hublink.Sent{}, fmt.Errorf("the hub sends as one of its users, user:<id>, not as %q", actor)
	}
	if key == "" {
		return hublink.Sent{}, errors.New("a message from the hub needs a key: without one a retry is sent twice")
	}
	body, _ := json.Marshal(map[string]any{"to": "agent:" + agent, "from": actor, "text": text, "key": key, "interrupt": interrupt})
	data, err := stack.ControlRequest(ctx, d.client, project, "POST", "/v1/messages", body)
	if err != nil {
		return hublink.Sent{}, err
	}
	var sent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &sent); err != nil || sent.ID == "" {
		return hublink.Sent{}, fmt.Errorf("the control sidecar said %q", strings.TrimSpace(string(data)))
	}
	return hublink.Sent{ID: sent.ID}, nil
}

// Resolve is one of the hub's users answering a question, or closing a request, addressed to them.
func (d *Engine) Resolve(ctx context.Context, project, message, actor, text, outcome string) error {
	if !isUser(actor) {
		return fmt.Errorf("the hub answers as one of its users, user:<id>, not as %q", actor)
	}
	if outcome == "" {
		outcome = "done"
	}
	body, _ := json.Marshal(map[string]string{"actor": actor, "text": text, "outcome": outcome})
	_, err := stack.ControlRequest(ctx, d.client, project, "POST", "/v1/messages/"+url.PathEscape(message)+"/resolve", body)
	return err
}

// Undelivered is the hub saying that an agent's message to one of its users did not reach them: the agent is told why.
func (d *Engine) Undelivered(ctx context.Context, project, message, reason string) error {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	_, err := stack.ControlRequest(ctx, d.client, project, "POST", "/v1/messages/"+url.PathEscape(message)+"/undelivered", body)
	return err
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

// SetPreviewTemplate hands the control sidecar what the hub said a preview's address is. Empty clears it.
func (d *Engine) SetPreviewTemplate(ctx context.Context, project, template string) error {
	return stack.SetPreviewTemplate(ctx, d.client, project, template)
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

// Workspaces lists the project's workspace volumes, from their labels: a directory's volume, a git workspace's base clone and the
// worktrees of its instances, each with the state of its instance. It reads no file.
func (d *Engine) Workspaces(ctx context.Context, project string) ([]hublink.Workspace, error) {
	volumes, err := d.client.API.VolumeList(ctx, volume.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"=workspace"))})
	if err != nil {
		return nil, err
	}
	agents, err := d.client.API.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"="+kindAgent))})
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, a := range agents {
		state := hublink.WorktreeStopped
		if a.State == "running" {
			state = hublink.WorktreeRunning
		}
		states[a.Labels[engine.LabelInstance]] = state
	}
	byName := map[string]*hublink.Workspace{}
	for _, v := range volumes.Volumes {
		id, err := engine.IdentityOf(v.Labels)
		if err != nil || id.Project != project || id.Workspace == "" {
			continue
		}
		ws := byName[id.Workspace]
		if ws == nil {
			ws = &hublink.Workspace{Name: id.Workspace, Kind: hublink.WorkspaceDirectory, Worktrees: []hublink.Worktree{}}
			byName[id.Workspace] = ws
		}
		switch {
		case id.Instance != "":
			ws.Kind = hublink.WorkspaceGit
			state, ok := states[id.Instance]
			if !ok {
				state = hublink.WorktreeRemoved
			}
			ws.Worktrees = append(ws.Worktrees, hublink.Worktree{Instance: id.Instance, State: state, Actor: id.Actor})
		case v.Name == stack.BaseVolume(project, id.Workspace):
			ws.Kind = hublink.WorkspaceGit
		}
	}
	remotes := d.remotes(ctx, project)
	list := make([]hublink.Workspace, 0, len(byName))
	for _, ws := range byName {
		sort.Slice(ws.Worktrees, func(i, j int) bool { return ws.Worktrees[i].Instance < ws.Worktrees[j].Instance })
		if git, ok := remotes[ws.Name]; ok && ws.Kind == hublink.WorkspaceGit {
			ws.Remote, ws.Branch = git.URL, git.Branch
		}
		list = append(list, *ws)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// remotes are where the git workspaces clone from, read from the templates `up` last published, in one question to control: a base
// clone is made only for a workspace an agent lists. A project whose control does not answer has none: the list does not wait on them.
func (d *Engine) remotes(ctx context.Context, project string) map[string]config.Git {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return nil
	}
	return remotesOf(published)
}

// remotesOf reads the git sources of the published templates. A URL is kept only as an https URL with a host, and without what could
// carry a secret: credentials, a query, a fragment. The project's config refuses credentials already; this does not count on it.
func remotesOf(published stack.Published) map[string]config.Git {
	remotes := map[string]config.Git{}
	for _, template := range published.Templates {
		for name, ws := range template.Workspaces {
			if ws.Git == nil {
				continue
			}
			u, err := url.Parse(ws.Git.URL)
			if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
				continue
			}
			u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
			remotes[name] = config.Git{URL: u.String(), Branch: ws.Git.Branch}
		}
	}
	return remotes
}

// Files carries one connection to the file server of a workspace volume: a directory's, or the worktree of instance. It makes the volume's
// file helper when it has none, and runs `egzo files connect` in it.
func (d *Engine) Files(ctx context.Context, project, workspace, instance string, in io.Reader, out, said io.Writer) error {
	if !names.Identifier(workspace) || instance != "" && !names.Instance(instance) {
		return fmt.Errorf("no workspace %q", workspace)
	}
	env := func(ctx context.Context) (stack.PrepEnv, error) {
		published, err := stack.ReadPublished(ctx, d.client, project)
		if err != nil {
			return stack.PrepEnv{}, err
		}
		return stack.PrepEnv{Image: published.Image, User: published.User}, nil
	}
	target := stack.FilesTarget{Workspace: workspace, Instance: instance}
	for attempt := 0; ; attempt++ {
		id, err := stack.FileHelper(ctx, d.client, env, project, target)
		if err != nil {
			return err
		}
		attempted := newFilesAttempt(in, out, said)
		code, err := d.client.ExecStream(ctx, id, stack.FilesConnectCommand, engine.Stream{In: attempted.in, Out: attempted.out, Err: attempted.said}, nil)
		attempted.end()
		if err == nil && code != 0 {
			err = fmt.Errorf("the file connection ended with code %d", code)
		}
		if err == nil || attempted.crossed() || ctx.Err() != nil || attempt == 1 {
			return err
		}
		// nothing crossed: the helper may have idled out between finding it and the exec. Make another, once
	}
}

// filesAttempt is one try at carrying a connection to a file server. The hub sends nothing before the link says the server was reached,
// so until then the attempt reads nothing of the hub's side: one that fails before it may be followed by another, which reads it all.
type filesAttempt struct {
	in        io.Reader
	out, said io.Writer
	reached   chan struct{}
	once      sync.Once
	ended     chan struct{}
}

func newFilesAttempt(in io.Reader, out, said io.Writer) *filesAttempt {
	a := &filesAttempt{reached: make(chan struct{}), ended: make(chan struct{})}
	a.in = gatedReader{in: in, open: a.reached, closed: a.ended}
	a.out = reachedWriter{Writer: out, reach: a.reach}
	a.said = announceWriter{Writer: said, reach: a.reach}
	return a
}

func (a *filesAttempt) reach() { a.once.Do(func() { close(a.reached) }) }

// end lets go of the hub's side, if the attempt never reached the server.
func (a *filesAttempt) end() { close(a.ended) }

// crossed says the server was reached: what the hub sent may have gone to it, and what it answered to the hub.
func (a *filesAttempt) crossed() bool {
	select {
	case <-a.reached:
		return true
	default:
		return false
	}
}

// gatedReader reads in only once open is closed. If closed is closed first, it ends without reading.
type gatedReader struct {
	in           io.Reader
	open, closed <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	select {
	case <-g.open:
		return g.in.Read(p)
	default:
	}
	select {
	case <-g.open:
		return g.in.Read(p)
	case <-g.closed:
		return 0, io.EOF
	}
}

// reachedWriter is the server's answer: its first byte says it was reached.
type reachedWriter struct {
	io.Writer
	reach func()
}

func (w reachedWriter) Write(p []byte) (int, error) {
	w.reach()
	return w.Writer.Write(p)
}

// announceWriter is what `egzo files connect` says on stderr: the announce says it reached the server.
type announceWriter struct {
	io.Writer
	reach func()
}

func (w announceWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), files.Connected) {
		w.reach()
	}
	return w.Writer.Write(p)
}
