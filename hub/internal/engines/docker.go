package engines

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
)

// Docker is an Engine on a Docker-compatible API, built on core's packages: what the CLI does, minus everything that reads
// a file of the host.
type Docker struct {
	client  *engine.Client
	closers []func() // what else to close: an ssh connection
}

// NewDocker wraps an open core engine client.
func NewDocker(c *engine.Client) *Docker { return &Docker{client: c} }

func (d *Docker) Close() error {
	err := d.client.Close()
	for _, f := range d.closers {
		f()
	}
	return err
}

// Client is the underlying connection, for the caller that opened it.
func (d *Docker) Client() *engine.Client { return d.client }

const (
	kindAgent   = "agent"
	kindControl = "control"
	kindProxy   = "proxy"
)

func (d *Docker) Projects(ctx context.Context) ([]Project, error) {
	list, err := d.client.API.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", engine.LabelProject))})
	if err != nil {
		return nil, err
	}
	byName := map[string]*Project{}
	for _, item := range list {
		name := item.Labels[engine.LabelProject]
		kind := item.Labels[engine.LabelKind]
		if name == "" || kind == "lock" {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &Project{Name: name, Dir: item.Labels[engine.LabelProjectDir], CreatedBy: item.Labels[engine.LabelCreatedBy]}
			byName[name] = p
		}
		switch kind {
		case kindAgent:
			p.Agents = append(p.Agents, Agent{
				Name: item.Labels[engine.LabelInstance], Template: item.Labels[engine.LabelService], State: item.State,
				Actor: item.Labels[engine.LabelActor], Created: time.Unix(item.Created, 0), Activity: activityOf(item.State, ""),
			})
		case kindControl, kindProxy:
			p.Services = append(p.Services, Service{Name: kind, State: item.State})
		}
	}
	projects := make([]Project, 0, len(byName))
	for _, p := range byName {
		p.Up = sidecarsRunning(p.Services)
		sort.Slice(p.Agents, func(i, j int) bool { return p.Agents[i].Name < p.Agents[j].Name })
		projects = append(projects, *p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func sidecarsRunning(services []Service) bool {
	running := map[string]bool{}
	for _, s := range services {
		running[s.Name] = s.State == "running"
	}
	return running[kindControl] && running[kindProxy]
}

func activityOf(state, reported string) string {
	switch {
	case state != "running":
		return "stopped"
	case reported == "":
		return "starting"
	}
	return reported
}

func (d *Docker) Project(ctx context.Context, name string) (Project, error) {
	observed, err := stack.Observe(ctx, d.client, name)
	if err != nil {
		return Project{}, err
	}
	project := Project{Name: name}
	var published *stack.Published
	if p, err := stack.ReadPublished(ctx, d.client, name); err == nil {
		published = &p
		project.Templates = p.Names()
	}
	rows := stack.Rows(observed, d.reported(ctx, name), published)
	for _, r := range observed.Resources {
		if r.Type == "container" && project.Dir == "" {
			project.Dir, project.CreatedBy = r.ProjectDir, ""
		}
	}
	if len(rows) == 0 {
		return Project{}, ErrNoProject{Name: name}
	}
	for _, row := range rows {
		switch row.Kind {
		case kindAgent:
			project.Agents = append(project.Agents, Agent{
				Name: row.Name, Template: row.Service, State: row.State, Health: row.Health, Activity: row.Activity, Status: row.Status,
				Open: row.Open, Waiting: row.Waiting, Actor: row.Actor, Stale: row.Stale, Created: row.Created,
			})
		case kindControl, kindProxy:
			project.Services = append(project.Services, Service{Name: row.Kind, State: row.State, Health: row.Health})
		}
	}
	project.Up = sidecarsRunning(project.Services)
	sort.Slice(project.Agents, func(i, j int) bool { return project.Agents[i].Name < project.Agents[j].Name })
	return project, nil
}

// reported asks the control sidecar what each agent last said about itself; best effort, like `egzo ps`.
func (d *Docker) reported(ctx context.Context, project string) map[string]stack.Report {
	reported := map[string]stack.Report{}
	reply, err := stack.ControlRequest(ctx, d.client, project, "GET", "/v1/agents", nil)
	if err != nil {
		return reported
	}
	var statuses []struct {
		Agent, Status, Activity string
		Open                    int
		Waiting                 bool
	}
	if json.Unmarshal(reply, &statuses) == nil {
		for _, s := range statuses {
			reported[s.Agent] = stack.Report{Status: s.Status, Activity: s.Activity, Open: s.Open, Waiting: s.Waiting}
		}
	}
	return reported
}

// instance finds an agent's container.
func (d *Docker) instance(ctx context.Context, project, agent string) (stack.Observed, *stack.Resource, error) {
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

func (d *Docker) Spawn(ctx context.Context, project, template, name, actor string) (string, error) {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return "", err
	}
	release, err := stack.LockWait(ctx, d.client, project, published.Image, 30*time.Second)
	if err != nil {
		return "", err
	}
	defer release()
	spawned, err := stack.Spawn(ctx, d.client, project, "", stack.SpawnRequest{Template: template, Name: name, Actor: actor}, io.Discard)
	if err != nil {
		return "", err
	}
	return spawned.Name, nil
}

func (d *Docker) Start(ctx context.Context, project, agent string) error {
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

func (d *Docker) Stop(ctx context.Context, project, agent string) error {
	_, target, err := d.instance(ctx, project, agent)
	if err != nil {
		return err
	}
	timeout := 5
	return d.client.API.ContainerStop(ctx, target.ID, container.StopOptions{Timeout: &timeout})
}

func (d *Docker) Remove(ctx context.Context, project, agent string) error {
	published, err := stack.ReadPublished(ctx, d.client, project)
	if err != nil {
		return err
	}
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
	_, err = stack.RemoveInstances(ctx, d.client, observed, env, project, published.Dir, []string{agent}, true, io.Discard)
	return err
}

// Attach runs `egzo agent attach` in the agent's container on a terminal, as `egzo attach` does.
func (d *Docker) Attach(ctx context.Context, project, agent string, term Terminal) error {
	_, target, err := d.instance(ctx, project, agent)
	if err != nil {
		return err
	}
	if target.State != "running" {
		return fmt.Errorf("agent %q is %s", agent, target.State)
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
var noisy = map[string]bool{"hook": true}

// withText are the types whose text is shown: what the agent says about itself. The text of a message never is.
var withText = map[string]bool{"status": true, "activity": true}

func (d *Docker) Events(ctx context.Context, project, agent string, limit int) ([]Event, error) {
	path := "/v1/events"
	if agent != "" {
		path += "?agent=" + agent
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

func (d *Docker) Logs(ctx context.Context, project, name string, tail int) (string, error) {
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
	if _, err := stdcopy.StdCopy(&out, &out, io.LimitReader(reader, 4<<20)); err != nil && !errors.Is(err, io.EOF) {
		// a container with a terminal logs one raw stream
		return "", err
	}
	return out.String(), nil
}

// Spec returns the resolved snapshot `up` stored on the control volume: references to secrets, never their values.
func (d *Docker) Spec(ctx context.Context, project string) ([]byte, error) {
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

func (d *Docker) currentHash(ctx context.Context, project string, hashes []string) string {
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
