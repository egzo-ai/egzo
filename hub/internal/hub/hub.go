// Package hub is the hub's own logic: discovery over several engines, the operators' access, and every operation a user asks for, each
// checked against the roles and written to the audit log. It speaks to engines only through engines.Engine.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var (
	// ErrForbidden is returned when the user may see the project but not do this.
	ErrForbidden = errors.New("you are not allowed to do that")
	// ErrNotFound is returned for what does not exist, and for a project the user has no role on: its existence is not leaked.
	ErrNotFound = errors.New("not found")
	// ErrOperatorAccess is returned when somebody tries to give, change or remove an operator's role on a project: an operator is not
	// granted a role, they hold every project.
	ErrOperatorAccess = errors.New("an operator holds every project: that is changed under Settings, Users, not as a role")
	// ErrLastOperator is returned when a change would leave the hub with no operator.
	ErrLastOperator = store.ErrLastOperator
)

// Hub ties the database, the engines and the rules together.
type Hub struct {
	store    *store.Store
	registry *engines.Registry
	log      *slog.Logger

	mu       sync.RWMutex
	scanned  map[string]*engineScan // by engine name
	attached map[string]*Attachment // who holds the terminal, by "engine:project/agent"
	feeds    *feedManager           // the status followers and event watchers; nil until StartFeeds
	pubMu    sync.Mutex
	pub      map[projectKey]*publishedEntry
	signIn   SignInOptions // set by SetupSignIn
	tasks    taskList      // what the hub does on the engines (tasks.go)
}

type engineScan struct {
	projects []engines.Project
	err      error
	at       time.Time
}

// New builds a hub; log may be nil.
func New(s *store.Store, r *engines.Registry, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{store: s, registry: r, log: log, scanned: map[string]*engineScan{}, attached: map[string]*Attachment{}, pub: map[projectKey]*publishedEntry{}}
}

// Store gives the web layer the database for what is not an operation: login, sessions.
func (h *Hub) Store() *store.Store { return h.store }

// Registry gives the web layer the engines for their management pages.
func (h *Hub) Registry() *engines.Registry { return h.registry }

// --- discovery ---------------------------------------------------------------------------------------------------------------

// Scan asks every engine for its projects and keeps what it learns. An engine that fails is recorded as failed and does not stop
// the others.
func (h *Hub) Scan(ctx context.Context) error {
	rows, err := h.store.Engines(ctx)
	if err != nil {
		return err
	}
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}
	results := h.listEngines(ctx, names)
	h.mu.Lock()
	h.scanned = results
	h.mu.Unlock()
	h.reconcileFeeds(names)
	return nil
}

// scanEngines lists some engines again and leaves what is known of the others as it is.
func (h *Hub) scanEngines(ctx context.Context, names []string) error {
	rows, err := h.store.Engines(ctx)
	if err != nil {
		return err
	}
	var all, listed []string
	have := map[string]bool{}
	for _, row := range rows {
		all = append(all, row.Name)
		have[row.Name] = true
	}
	for _, name := range names {
		if have[name] {
			listed = append(listed, name)
		}
	}
	results := h.listEngines(ctx, listed)
	h.mu.Lock()
	for name := range h.scanned {
		if !have[name] {
			delete(h.scanned, name)
		}
	}
	for name, scan := range results {
		h.scanned[name] = scan
	}
	h.mu.Unlock()
	h.reconcileFeeds(all)
	return nil
}

// listEngines asks the named engines for their projects, all at once.
func (h *Hub) listEngines(ctx context.Context, names []string) map[string]*engineScan {
	results := make(map[string]*engineScan, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scan := &engineScan{at: time.Now()}
			eng, err := h.registry.Get(ctx, name)
			if err == nil {
				scan.projects, err = eng.Projects(ctx)
				if err != nil {
					h.registry.Failed(name, err)
				}
			}
			scan.err = err
			mu.Lock()
			results[name] = scan
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// reconcileFeeds lets the streams follow what the scans found.
func (h *Hub) reconcileFeeds(engineNames []string) {
	m := h.feedManager()
	if m == nil {
		return
	}
	h.mu.RLock()
	scanned := make(map[string]*engineScan, len(h.scanned))
	for name, scan := range h.scanned {
		scanned[name] = scan
	}
	h.mu.RUnlock()
	m.reconcile(scanned, engineNames)
}

// EngineView is one engine and what the last scan found on it.
type EngineView struct {
	Name, Host string
	Up         bool
	Err        string
	ScannedAt  time.Time
}

// Engines lists the engines with the state the last scan saw, without calling any engine.
func (h *Hub) Engines(ctx context.Context) ([]EngineView, error) {
	rows, err := h.store.Engines(ctx)
	if err != nil {
		return nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	views := make([]EngineView, 0, len(rows))
	for _, row := range rows {
		v := EngineView{Name: row.Name, Host: row.Host}
		if scan := h.scanned[row.Name]; scan != nil {
			v.Up, v.ScannedAt = scan.err == nil, scan.at
			if scan.err != nil {
				v.Err = scan.err.Error()
			}
		} else {
			v.Err = "not scanned yet"
		}
		views = append(views, v)
	}
	return views, nil
}

// ProjectRef is a project on an engine, with what the user holds on it. For an operator Role is owner and Operator is set: they
// hold the project without a grant.
type ProjectRef struct {
	Engine   string
	Project  engines.Project
	Role     rbac.Role
	Operator bool
	// Feed says how far what the agents are doing can be trusted.
	Feed Feed
	// Tasks are what the hub does on the project's agents now.
	Tasks []Task
}

// Projects lists the projects the user holds a role on (an operator: every project), across engines, from the last scan. unreachable lists the engines that
// did not answer, so the page can say so without dropping what the others returned.
func (h *Hub) Projects(ctx context.Context, user store.User) (refs []ProjectRef, unreachable []EngineView, err error) {
	grants, err := h.store.GrantsOf(ctx, user.ID)
	if err != nil {
		return nil, nil, err
	}
	roles := map[[2]string]rbac.Role{}
	for _, g := range grants {
		roles[[2]string{g.Engine, g.Project}] = g.Role
	}
	views, err := h.Engines(ctx)
	if err != nil {
		return nil, nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, v := range views {
		scan := h.scanned[v.Name]
		if scan == nil {
			continue
		}
		if scan.err != nil {
			if user.IsOperator || hasRoleOnEngine(roles, v.Name) {
				unreachable = append(unreachable, v)
			}
			continue
		}
		for _, p := range scan.projects {
			role, ok := roles[[2]string{v.Name, p.Name}]
			if user.IsOperator {
				role, ok = rbac.Owner, true
			}
			if ok {
				shown, feed := h.overlay(v.Name, p)
				refs = append(refs, ProjectRef{Engine: v.Name, Project: shown, Role: role, Operator: user.IsOperator, Feed: feed, Tasks: h.runningTasks(v.Name, p.Name)})
			}
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Project.Name != refs[j].Project.Name {
			return refs[i].Project.Name < refs[j].Project.Name
		}
		return refs[i].Engine < refs[j].Engine
	})
	return refs, unreachable, nil
}

func hasRoleOnEngine(roles map[[2]string]rbac.Role, engine string) bool {
	for key := range roles {
		if key[0] == engine {
			return true
		}
	}
	return false
}

// --- one project -------------------------------------------------------------------------------------------------------------

// AgentView is an agent with what the user may do to it.
type AgentView struct {
	engines.Agent
	CanOperate bool
	// Holder is who holds the terminal, "" when nobody.
	Holder string
	// Denied is how many hosts the proxy refused the agent in the last hour, and that it has not reached since.
	Denied int
	// Task is what the hub does on the agent, or the failure of what it did that nobody dismissed; nil when neither.
	Task *TaskView
}

// ProjectView is the project page.
type ProjectView struct {
	Engine   string
	Project  engines.Project
	Role     rbac.Role
	Operator bool // the user holds the project as an operator, not through a grant
	// Feed says how far what the agents are doing can be trusted.
	Feed      Feed
	Agents    []AgentView
	CanSpawn  bool
	CanManage bool
}

// access finds the user's role on a project, or ErrNotFound when they have none. An operator holds every project: they are owner
// without a grant.
func (h *Hub) access(ctx context.Context, user store.User, engine, project string) (rbac.Role, error) {
	if user.IsOperator {
		return h.operatorAccess(ctx, engine, project)
	}
	role, err := h.store.Role(ctx, engine, project, user.ID)
	if err != nil {
		return rbac.None, err
	}
	if role == rbac.None {
		return rbac.None, ErrNotFound
	}
	return role, nil
}

// operatorAccess is the operator's way in: the engine must be one the hub has, and when its last scan listed the projects, the
// project must be one of them. An engine that did not answer is not an excuse to refuse: the call itself says what is wrong.
func (h *Hub) operatorAccess(ctx context.Context, engine, project string) (rbac.Role, error) {
	if _, err := h.store.Engine(ctx, engine); errors.Is(err, store.ErrNotFound) {
		return rbac.None, ErrNotFound
	} else if err != nil {
		return rbac.None, err
	}
	h.mu.RLock()
	scan := h.scanned[engine]
	h.mu.RUnlock()
	if scan != nil && scan.err == nil {
		for _, p := range scan.projects {
			if p.Name == project {
				return rbac.Owner, nil
			}
		}
		return rbac.None, ErrNotFound
	}
	return rbac.Owner, nil
}

func (h *Hub) engine(ctx context.Context, name string) (engines.Engine, error) {
	eng, err := h.registry.Get(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return eng, err
}

// call runs f on the engine and, when it fails because the engine is unreachable rather than because of what was asked, drops
// the connection so the next call reconnects.
func (h *Hub) call(ctx context.Context, name string, f func(engines.Engine) error) error {
	eng, err := h.engine(ctx, name)
	if err != nil {
		return err
	}
	err = f(eng)
	var gone engines.ErrNoProject
	if err != nil && !errors.As(err, &gone) && ctx.Err() == nil && isConnectionError(err) {
		h.registry.Failed(name, err)
	}
	return err
}

// Project reads one project. The containers come from the last scan, which the engine's events keep current, so a page that polls
// asks the engine for nothing; a project the scan does not list (it is newer than the scan) is read from the engine. The agents'
// activity comes from the status feed, and the templates from the sidecar, kept for a few minutes.
func (h *Hub) Project(ctx context.Context, user store.User, engine, name string) (ProjectView, error) {
	role, err := h.access(ctx, user, engine, name)
	if err != nil {
		return ProjectView{}, err
	}
	project, found := h.scannedProject(engine, name)
	if !found {
		err = h.call(ctx, engine, func(e engines.Engine) (err error) {
			project, err = e.Project(ctx, name)
			return err
		})
		var gone engines.ErrNoProject
		if errors.As(err, &gone) {
			return ProjectView{}, ErrNotFound
		}
		if err != nil {
			return ProjectView{}, err
		}
	}
	published, ok := h.publishedFor(ctx, engine, project)
	project = withTemplates(project, published, ok)
	project, feed := h.overlay(engine, project)
	view := ProjectView{Engine: engine, Project: project, Role: role, Operator: user.IsOperator, Feed: feed,
		CanSpawn: rbac.Can(user.Subject(), role, rbac.Spawn, ""), CanManage: rbac.Can(user.Subject(), role, rbac.ManageRoles, "")}
	denied := h.deniedCounts(engine, name)
	for _, a := range project.Agents {
		av := AgentView{Agent: a, CanOperate: rbac.Can(user.Subject(), role, rbac.Operate, a.Actor)}
		av.Holder = h.holder(engine, name, a.Name)
		if a.ID != "" && (role.AtLeast(rbac.Administrator) || av.CanOperate) {
			av.Denied = denied[a.ID]
		}
		view.Agents = append(view.Agents, av)
	}
	view.Agents = h.withTasks(user, role, engine, name, view.Agents)
	return view, nil
}

// scannedProject is a project as the last scan of its engine saw it.
func (h *Hub) scannedProject(engine, name string) (engines.Project, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	scan := h.scanned[engine]
	if scan == nil || scan.err != nil {
		return engines.Project{}, false
	}
	for _, p := range scan.projects {
		if p.Name == name {
			return p, true
		}
	}
	return engines.Project{}, false
}

// Agent reads one agent of a project.
func (h *Hub) Agent(ctx context.Context, user store.User, engine, project, agent string) (ProjectView, AgentView, error) {
	view, err := h.Project(ctx, user, engine, project)
	if err != nil {
		return view, AgentView{}, err
	}
	for _, a := range view.Agents {
		if a.Name == agent {
			return view, a, nil
		}
	}
	return view, AgentView{}, ErrNotFound
}

// authorize checks one action, writes the audit entry either way, and returns the role.
func (h *Hub) authorize(ctx context.Context, user store.User, engine, project, agent string, action rbac.Action, creator, audit string) (rbac.Role, error) {
	role, err := h.access(ctx, user, engine, project)
	if err != nil {
		return rbac.None, err
	}
	allowed := rbac.Can(user.Subject(), role, action, creator)
	entry := store.AuditEntry{Actor: rbac.Actor(user.ID), Action: audit, Engine: engine, Project: project, Agent: agent, Allowed: allowed}
	if !allowed {
		entry.Detail = fmt.Sprintf("refused: %s needs more than %s", action, role)
	}
	if err := h.store.Audit(ctx, entry); err != nil {
		return role, err
	}
	if !allowed {
		return role, ErrForbidden
	}
	return role, nil
}

// creatorOf reads who spawned an agent, from the engine.
func (h *Hub) creatorOf(ctx context.Context, engine, project, agent string) (string, error) {
	var found string
	err := h.call(ctx, engine, func(e engines.Engine) error {
		p, err := e.Project(ctx, project)
		if err != nil {
			return err
		}
		for _, a := range p.Agents {
			if a.Name == agent {
				found = a.Actor
				return nil
			}
		}
		return ErrNotFound
	})
	var gone engines.ErrNoProject
	if errors.As(err, &gone) {
		return "", ErrNotFound
	}
	return found, err
}

// afterOperation refreshes what the next page shows.
func (h *Hub) afterOperation(ctx context.Context, engine string) {
	eng, err := h.registry.Get(ctx, engine)
	if err != nil {
		return
	}
	projects, err := eng.Projects(ctx)
	h.mu.Lock()
	if h.scanned[engine] != nil && err == nil {
		h.scanned[engine] = &engineScan{projects: projects, at: time.Now()}
	}
	h.mu.Unlock()
}

// Events returns the timeline of a project, or of one agent.
func (h *Hub) Events(ctx context.Context, user store.User, engine, project, agent string, limit int) ([]engines.Event, error) {
	if _, err := h.access(ctx, user, engine, project); err != nil {
		return nil, err
	}
	var events []engines.Event
	err := h.call(ctx, engine, func(e engines.Engine) (err error) {
		events, err = e.Events(ctx, project, agent, limit)
		return err
	})
	return events, err
}

// Logs returns the end of a container's log: an agent or a sidecar. Administrators only: a log can show what an agent printed
// from another user's session.
func (h *Hub) Logs(ctx context.Context, user store.User, engine, project, container string, tail int) (string, error) {
	role, err := h.access(ctx, user, engine, project)
	if err != nil {
		return "", err
	}
	if !role.AtLeast(rbac.Administrator) {
		creator, err := h.creatorOf(ctx, engine, project, container)
		if err != nil || !rbac.Can(user.Subject(), role, rbac.Operate, creator) {
			return "", ErrForbidden
		}
	}
	var out string
	err = h.call(ctx, engine, func(e engines.Engine) (err error) {
		out, err = e.Logs(ctx, project, container, tail)
		return err
	})
	return out, err
}

// Spec returns the project's resolved snapshot, read only.
func (h *Hub) Spec(ctx context.Context, user store.User, engine, project string) ([]byte, error) {
	if _, err := h.access(ctx, user, engine, project); err != nil {
		return nil, err
	}
	var spec []byte
	err := h.call(ctx, engine, func(e engines.Engine) (err error) {
		spec, err = e.Spec(ctx, project)
		return err
	})
	return spec, err
}

// Audit lists the latest entries about a project, for its administrators.
func (h *Hub) Audit(ctx context.Context, user store.User, engine, project string, limit int) ([]store.AuditEntry, error) {
	role, err := h.access(ctx, user, engine, project)
	if err != nil {
		return nil, err
	}
	if !role.AtLeast(rbac.Administrator) {
		return nil, ErrForbidden
	}
	return h.store.AuditOf(ctx, engine, project, limit)
}

func isConnectionError(err error) bool {
	var hostKey *engines.HostKeyError
	if errors.As(err, &hostKey) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, part := range []string{"connection refused", "connection reset", "broken pipe", "EOF", "no such host", "i/o timeout",
		"Cannot connect to the Docker daemon", "no container engine reachable"} {
		if strings.Contains(err.Error(), part) {
			return true
		}
	}
	return false
}
