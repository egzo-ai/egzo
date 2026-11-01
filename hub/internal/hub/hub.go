// Package hub is the hub's own logic: the projects that linked, the operators' oversight, and every operation a user asks for, each checked
// against the roles and written to the audit log. It speaks to projects only through links.Engine.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

var (
	// ErrForbidden is returned when the user may see the project but not do this.
	ErrForbidden = errors.New("you are not allowed to do that")
	// ErrNotFound is returned for what does not exist, and for a project the user has no role on: its existence is not leaked.
	ErrNotFound = errors.New("not found")
	// ErrLastOperator is returned when a change would leave the hub with no operator.
	ErrLastOperator = store.ErrLastOperator
	// ErrLastOwner is returned when a change would leave a project without a valid owner.
	ErrLastOwner = store.ErrLastOwner
	// ErrOverQuota is returned to a person who has as many projects as their quota allows.
	ErrOverQuota = store.ErrOverQuota
)

// Hub ties the database, the links and the rules together.
type Hub struct {
	store *store.Store
	log   *slog.Logger

	linking  sync.Mutex // a connection's arrival and its end are one step each (links.go)
	mu       sync.RWMutex
	conns    map[string]*connection  // the links connected now, by project id
	held     map[string]*heldProject // what the links last pushed, by project id
	attached map[string]*Attachment  // who holds the terminal, by "project/agent"
	feeds    *feedManager            // the status and audit trails the links push
	signIn   SignInOptions           // set by SetupSignIn
	tasks    taskList                // what the hub does on the projects (tasks.go)
	previews previewSite             // set by ServePreviews

	previewMu  sync.Mutex
	previewing map[*openPreview]struct{} // what preview servers are answering now
}

// New builds a hub; log may be nil.
func New(s *store.Store, log *slog.Logger, opt FeedOptions) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &Hub{store: s, log: log, conns: map[string]*connection{}, held: map[string]*heldProject{}, attached: map[string]*Attachment{}}
	h.feeds = newFeedManager(h, opt)
	return h
}

// Store gives the web layer the database for what is not an operation: login, sessions.
func (h *Hub) Store() *store.Store { return h.store }

// --- the projects ----------------------------------------------------------------------------------------------------------------

// ProjectRef is a project with what the user holds on it. An operator with no role on it has rbac.Overseer.
type ProjectRef struct {
	ID      string
	Host    string
	Project links.Project
	Role    rbac.Role
	// Ownerless says nobody who can sign in owns it, for an operator to see.
	Ownerless bool
	// Offline is when its link went away; zero while it is connected.
	Offline time.Time
	// Feed says how far what the agents are doing can be trusted.
	Feed Feed
	// Tasks are what the hub does on the project's agents now.
	Tasks []Task
}

// Projects lists the projects the user sees (an operator: every project), offline ones included, by name. A viewer does not see theirs.
func (h *Hub) Projects(ctx context.Context, user store.User) ([]ProjectRef, error) {
	grants, err := h.store.GrantsOf(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	roles := map[string]rbac.Role{}
	for _, g := range grants {
		roles[g.Project] = g.Role
	}
	regs, err := h.store.ApprovedProjects(ctx)
	if err != nil {
		return nil, err
	}
	var owners map[string][]string
	if user.IsOperator {
		if owners, err = h.store.ValidOwners(ctx); err != nil {
			return nil, err
		}
	}
	refs := []ProjectRef{}
	for _, reg := range regs {
		if role := seen(user, roles[reg.ID]); role != rbac.None {
			shown, feed := h.overlay(reg, h.heldOf(reg))
			refs = append(refs, ProjectRef{ID: reg.ID, Host: reg.Host, Project: shown, Role: role, Ownerless: user.IsOperator && len(owners[reg.ID]) == 0,
				Offline: reg.Offline, Feed: feed, Tasks: h.runningTasks(reg.ID)})
		}
	}
	return refs, nil
}

// seen is what a grant shows the user of a project. A viewer holds a role and is shown nothing: to them it is a project they have no role on.
func seen(user store.User, role rbac.Role) rbac.Role {
	switch {
	case role != rbac.None && role != rbac.Viewer:
		return role
	case user.IsOperator:
		return rbac.Overseer
	}
	return rbac.None
}

// heldOf is a project as its link last said it was, with the name it registered under: the name is the hub's to show, not an update's.
func (h *Hub) heldOf(reg store.Registration) links.Project {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p := links.Project{Name: reg.Name, Templates: reg.Templates}
	if held := h.held[reg.ID]; held != nil {
		p = held.project
		p.Name = reg.Name
	}
	return p
}

// --- one project -------------------------------------------------------------------------------------------------------------

// AgentView is an agent with what the user may do to it.
type AgentView struct {
	links.Agent
	CanOperate bool
	// Holder is who holds the terminal, "" when nobody.
	Holder string
	// Denied is how many hosts the proxy refused the agent in the last hour, and that it has not reached since.
	Denied int
	// Task is what the hub does on the agent, or the failure of what it did that nobody dismissed; nil when neither.
	Task *TaskView
	// Previews are the ports it exposed.
	Previews []PreviewView
}

// ProjectView is the project page.
type ProjectView struct {
	ID      string
	Host    string
	Project links.Project
	Role    rbac.Role
	// Ownerless says nobody who can sign in owns it, for an operator to see.
	Ownerless bool
	// Offline is when its link went away; zero while it is connected.
	Offline time.Time
	// Feed says how far what the agents are doing can be trusted.
	Feed       Feed
	Agents     []AgentView
	CanSpawn   bool
	CanManage  bool // the user manages the roles and invites
	CanUnlink  bool
	CanMembers bool // the user sees who holds what
}

// registration reads a project the hub shows: one that was approved. Any other is not there, to anyone.
func (h *Hub) registration(ctx context.Context, id string) (store.Registration, error) {
	reg, err := h.store.Registration(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && reg.State != hublink.StateApproved {
		return reg, ErrNotFound
	}
	return reg, err
}

// access finds what the user holds on a project that is shown to them, or ErrNotFound when it is not: a role, or rbac.Overseer for an operator
// with none. A viewer's role is not this kind of access: nothing of the project is shown to them.
func (h *Hub) access(ctx context.Context, user store.User, id string) (rbac.Role, error) {
	if _, err := h.registration(ctx, id); err != nil {
		return rbac.None, err
	}
	role, err := h.store.Role(ctx, id, user.ID)
	if err != nil {
		return rbac.None, err
	}
	if role = seen(user, role); role == rbac.None {
		return rbac.None, ErrNotFound
	}
	return role, nil
}

// inspect is access for what takes a role: an operator who holds none sees the project and its agents, and no further.
func (h *Hub) inspect(ctx context.Context, user store.User, id string) (rbac.Role, error) {
	role, err := h.access(ctx, user, id)
	if err != nil {
		return rbac.None, err
	}
	if !rbac.Can(user.Subject(), role, rbac.Inspect, "") {
		return role, ErrForbidden
	}
	return role, nil
}

// readLimit is how long a link gets to answer what a page waits for. One that does not is somebody else's machine: the page says so
// and goes on.
const readLimit = 30 * time.Second

// call runs f on the project's link. What fails because the link is not there, or went away in between, is an OfflineError, whatever f
// was told: it is told apart from what the project refused. A connection that ends ends f, and f ended that way is an OfflineError even
// if it says nothing went wrong: a terminal returns nil when it is cut. caller ending it is the caller's own doing.
func (h *Hub) call(caller context.Context, id string, f func(context.Context, links.Engine) error) error {
	c := h.connected(id)
	if c == nil || !c.served.Load() {
		return h.offline(caller, id)
	}
	ctx, cancel := context.WithCancel(caller)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	err := f(ctx, c.link)
	if (err != nil || ctx.Err() != nil && caller.Err() == nil) && c.gone() {
		return OfflineError{Since: time.Now()}
	}
	return err
}

func (h *Hub) offline(ctx context.Context, id string) error {
	reg, err := h.store.Registration(ctx, id)
	if err != nil {
		return OfflineError{}
	}
	return OfflineError{Since: reg.Offline}
}

// Project reads one project. It asks the link nothing, so a page that polls costs the project nothing: what it shows is what the link
// last pushed. A project offline shows what was last known of it until the hub restarts, marked offline, and its name and host after.
func (h *Hub) Project(ctx context.Context, user store.User, id string) (ProjectView, error) {
	role, err := h.access(ctx, user, id)
	if err != nil {
		return ProjectView{}, err
	}
	reg, err := h.store.Registration(ctx, id)
	if err != nil {
		return ProjectView{}, err
	}
	project, feed := h.overlay(reg, h.heldOf(reg))
	view := ProjectView{ID: id, Host: reg.Host, Project: project, Role: role, Offline: reg.Offline, Feed: feed,
		CanSpawn: reg.Online() && rbac.Can(user.Subject(), role, rbac.Spawn, ""), CanManage: rbac.Can(user.Subject(), role, rbac.ManageRoles, ""),
		CanUnlink: rbac.Can(user.Subject(), role, rbac.Unlink, ""), CanMembers: rbac.Can(user.Subject(), role, rbac.SeeMembers, "")}
	if user.IsOperator {
		owners, err := h.store.ValidOwners(ctx)
		if err != nil {
			return ProjectView{}, err
		}
		view.Ownerless = len(owners[id]) == 0
	}
	denied := h.deniedCounts(id)
	for _, a := range project.Agents {
		av := AgentView{Agent: a, CanOperate: rbac.Can(user.Subject(), role, rbac.Operate, a.Actor)}
		av.Holder = h.holder(id, a.Name)
		if a.ID != "" && (role.AtLeast(rbac.Admin) || av.CanOperate) {
			av.Denied = denied[a.ID]
		}
		av.Previews = h.previewsOf(id, a.Name)
		view.Agents = append(view.Agents, av)
	}
	view.Agents = h.withTasks(user, role, id, view.Agents)
	return view, nil
}

// knownProject is a project as its link last pushed it, and whether it has.
func (h *Hub) knownProject(id string) (links.Project, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if held := h.held[id]; held != nil && held.known {
		return held.project, true
	}
	return links.Project{}, false
}

// Agent reads one agent of a project.
func (h *Hub) Agent(ctx context.Context, user store.User, id, agent string) (ProjectView, AgentView, error) {
	view, err := h.Project(ctx, user, id)
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
func (h *Hub) authorize(ctx context.Context, user store.User, id, agent string, action rbac.Action, creator, audit string) (rbac.Role, error) {
	role, err := h.access(ctx, user, id)
	if err != nil {
		return rbac.None, err
	}
	allowed := rbac.Can(user.Subject(), role, action, creator)
	entry := store.AuditEntry{Actor: rbac.Actor(user.ID), Action: audit, Project: id, Agent: agent, Allowed: allowed}
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

// record writes an entry for something that has been decided already: the action stands, so a failure is for the log.
func (h *Hub) record(ctx context.Context, entry store.AuditEntry) {
	if err := h.store.Audit(ctx, entry); err != nil {
		h.log.Error("the audit log was not written", "action", entry.Action, "actor", entry.Actor, "project", entry.Project, "err", err)
	}
}

// require checks one action that writes its own entry when it is done: only a refusal is written here.
func (h *Hub) require(ctx context.Context, user store.User, id string, action rbac.Action, audit string) error {
	role, err := h.access(ctx, user, id)
	if err != nil {
		return err
	}
	if !rbac.Can(user.Subject(), role, action, "") {
		h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: audit, Project: id, Detail: fmt.Sprintf("refused: %s needs more than %s", action, role)})
		return ErrForbidden
	}
	return nil
}

// creatorOf reads who spawned an agent, from the project.
func (h *Hub) creatorOf(ctx context.Context, id, agent string) (string, error) {
	var found string
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err := h.call(ctx, id, func(ctx context.Context, e links.Engine) error {
		p, err := e.Project(ctx)
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
	if errors.As(err, new(links.ErrNoProject)) {
		return "", ErrNotFound
	}
	return found, err
}

// Events returns the timeline of a project, or of one agent.
func (h *Hub) Events(ctx context.Context, user store.User, id, agent string, limit int) ([]links.Event, error) {
	if _, err := h.inspect(ctx, user, id); err != nil {
		return nil, err
	}
	var events []links.Event
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err := h.call(ctx, id, func(ctx context.Context, e links.Engine) (err error) {
		events, err = e.Events(ctx, agent, limit)
		return err
	})
	return events, err
}

// Logs returns the end of a container's log: an agent or a sidecar. Administrators only: a log can show what an agent printed
// from another user's session.
func (h *Hub) Logs(ctx context.Context, user store.User, id, container string, tail int) (string, error) {
	role, err := h.inspect(ctx, user, id)
	if err != nil {
		return "", err
	}
	if !role.AtLeast(rbac.Admin) {
		creator, err := h.creatorOf(ctx, id, container)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err // a project that cannot answer has refused nobody
		}
		if err != nil || !rbac.Can(user.Subject(), role, rbac.Operate, creator) {
			h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.logs", Project: id, Agent: container, Detail: fmt.Sprintf("refused: reading a log needs more than %s", role)})
			return "", ErrForbidden
		}
	}
	var out string
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err = h.call(ctx, id, func(ctx context.Context, e links.Engine) (err error) {
		out, err = e.Logs(ctx, container, tail)
		return err
	})
	return out, err
}

// Spec returns the project's resolved snapshot, read only.
func (h *Hub) Spec(ctx context.Context, user store.User, id string) ([]byte, error) {
	if _, err := h.inspect(ctx, user, id); err != nil {
		return nil, err
	}
	var spec []byte
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err := h.call(ctx, id, func(ctx context.Context, e links.Engine) (err error) {
		spec, err = e.Spec(ctx)
		return err
	})
	return spec, err
}

// Audit lists the latest entries about a project, for its administrators.
func (h *Hub) Audit(ctx context.Context, user store.User, id string, limit int) ([]store.AuditEntry, error) {
	role, err := h.access(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if !role.AtLeast(rbac.Admin) {
		h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "audit.read", Project: id, Detail: fmt.Sprintf("refused: the audit log needs more than %s", role)})
		return nil, ErrForbidden
	}
	return h.store.AuditOf(ctx, id, limit)
}

// HubAudit lists the entries of the whole hub, newest first: what has no project, and what is left of projects that are gone. Only an operator
// may, and with no role on the projects it covers: overseeing the hub takes reading what was done on it.
func (h *Hub) HubAudit(ctx context.Context, user store.User, before int64, limit int) ([]store.AuditEntry, error) {
	if err := h.requireOperator(ctx, user, "audit.read", ""); err != nil {
		return nil, err
	}
	return h.store.AuditAll(ctx, before, limit)
}
