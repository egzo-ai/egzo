// Package hub is the hub's own logic: discovery over several engines, orphan claims, and every operation a user asks for, each
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
)

// Hub ties the database, the engines and the rules together.
type Hub struct {
	store    *store.Store
	registry *engines.Registry
	log      *slog.Logger

	mu       sync.RWMutex
	scanned  map[string]*engineScan // by engine name
	attached map[string][]*Viewer   // by "engine:project/agent"
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
	return &Hub{store: s, registry: r, log: log, scanned: map[string]*engineScan{}, attached: map[string][]*Viewer{}}
}

// Store gives the web layer the database for what is not an operation: login, sessions.
func (h *Hub) Store() *store.Store { return h.store }

// Registry gives the web layer the engines for their management pages.
func (h *Hub) Registry() *engines.Registry { return h.registry }

// --- discovery ---------------------------------------------------------------------------------------------------------------

// Scan asks every engine for its projects, keeps what it learns, and gives the operator owner on every project nobody owns.
// An engine that fails is recorded as failed and does not stop the others.
func (h *Hub) Scan(ctx context.Context) error {
	rows, err := h.store.Engines(ctx)
	if err != nil {
		return err
	}
	results := make(map[string]*engineScan, len(rows))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scan := &engineScan{at: time.Now()}
			eng, err := h.registry.Get(ctx, row.Name)
			if err == nil {
				scan.projects, err = eng.Projects(ctx)
				if err != nil {
					h.registry.Failed(row.Name, err)
				}
			}
			scan.err = err
			mu.Lock()
			results[row.Name] = scan
			mu.Unlock()
		}()
	}
	wg.Wait()
	h.mu.Lock()
	h.scanned = results
	h.mu.Unlock()
	return h.claimOrphans(ctx)
}

// claimOrphans gives the operator owner on every discovered project that has no owner. It is the only special case in the
// permission model; everything after it is an ordinary role check.
func (h *Hub) claimOrphans(ctx context.Context) error {
	operator, err := h.store.Operator(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil // nobody has logged in yet: the first login claims
	}
	if err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for engine, scan := range h.scanned {
		if scan.err != nil {
			continue
		}
		for _, p := range scan.projects {
			claimed, err := h.store.ClaimIfOrphan(ctx, engine, p.Name, operator.ID)
			if err != nil {
				return err
			}
			if claimed {
				h.log.Info("operator owns an orphan project", "engine", engine, "project", p.Name)
				_ = h.store.Audit(ctx, store.AuditEntry{Actor: "hub", Action: "project.claim", Engine: engine, Project: p.Name, Detail: "no owner: the operator", Allowed: true})
			}
		}
	}
	return nil
}

// Login records a successful login. The first user becomes the operator and owns what is already discovered.
func (h *Hub) Login(ctx context.Context, identity, email, name string) (store.User, error) {
	user, created, err := h.store.LoginUser(ctx, identity, email, name)
	if err != nil {
		return store.User{}, err
	}
	if created && user.IsOperator {
		_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "user.operator", Detail: "the first login is the operator", Allowed: true})
		h.mu.RLock()
		scanned := len(h.scanned) > 0
		h.mu.RUnlock()
		if !scanned {
			// the first scan may not have run before the first login
			if err := h.Scan(ctx); err != nil {
				return user, err
			}
		} else if err := h.claimOrphans(ctx); err != nil {
			return user, err
		}
	}
	return user, nil
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

// ProjectRef is a project on an engine, with what the user holds on it.
type ProjectRef struct {
	Engine  string
	Project engines.Project
	Role    rbac.Role
}

// Projects lists the projects the user holds a role on, across engines, from the last scan. unreachable lists the engines that
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
			if hasRoleOnEngine(roles, v.Name) {
				unreachable = append(unreachable, v)
			}
			continue
		}
		for _, p := range scan.projects {
			if role, ok := roles[[2]string{v.Name, p.Name}]; ok {
				refs = append(refs, ProjectRef{Engine: v.Name, Project: p, Role: role})
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
	// Writer is who holds the terminal, "" when nobody.
	Writer string
	// Viewers is how many are watching.
	Viewers int
}

// ProjectView is the project page.
type ProjectView struct {
	Engine    string
	Project   engines.Project
	Role      rbac.Role
	Agents    []AgentView
	CanSpawn  bool
	CanManage bool
}

// access finds the user's role on a project, or ErrNotFound when they have none.
func (h *Hub) access(ctx context.Context, user store.User, engine, project string) (rbac.Role, error) {
	role, err := h.store.Role(ctx, engine, project, user.ID)
	if err != nil {
		return rbac.None, err
	}
	if role == rbac.None {
		return rbac.None, ErrNotFound
	}
	return role, nil
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

// Project reads one project live from its engine.
func (h *Hub) Project(ctx context.Context, user store.User, engine, name string) (ProjectView, error) {
	role, err := h.access(ctx, user, engine, name)
	if err != nil {
		return ProjectView{}, err
	}
	var project engines.Project
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
	view := ProjectView{Engine: engine, Project: project, Role: role,
		CanSpawn: rbac.Can(user.Subject(), role, rbac.Spawn, ""), CanManage: rbac.Can(user.Subject(), role, rbac.ManageRoles, "")}
	for _, a := range project.Agents {
		av := AgentView{Agent: a, CanOperate: rbac.Can(user.Subject(), role, rbac.Operate, a.Actor)}
		av.Writer, av.Viewers = h.presence(engine, name, a.Name)
		view.Agents = append(view.Agents, av)
	}
	return view, nil
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

// Spawn makes an agent from a template; it belongs to the user.
func (h *Hub) Spawn(ctx context.Context, user store.User, engine, project, template, name string) (string, error) {
	if _, err := h.authorize(ctx, user, engine, project, name, rbac.Spawn, "", "agent.spawn"); err != nil {
		return "", err
	}
	var spawned string
	err := h.call(ctx, engine, func(e engines.Engine) (err error) {
		spawned, err = e.Spawn(ctx, project, template, name, rbac.Actor(user.ID))
		return err
	})
	h.afterOperation(ctx, engine)
	return spawned, err
}

// Operate runs start, stop or rm on an agent.
func (h *Hub) Operate(ctx context.Context, user store.User, verb, engine, project, agent string) error {
	if verb != "start" && verb != "stop" && verb != "rm" {
		return fmt.Errorf("unknown operation %q", verb)
	}
	if _, err := h.access(ctx, user, engine, project); err != nil {
		return err
	}
	creator, err := h.creatorOf(ctx, engine, project, agent)
	if err != nil {
		return err
	}
	if _, err := h.authorize(ctx, user, engine, project, agent, rbac.Operate, creator, "agent."+verb); err != nil {
		return err
	}
	err = h.call(ctx, engine, func(e engines.Engine) error {
		switch verb {
		case "start":
			return e.Start(ctx, project, agent)
		case "stop":
			return e.Stop(ctx, project, agent)
		}
		return e.Remove(ctx, project, agent)
	})
	h.afterOperation(ctx, engine)
	return err
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
