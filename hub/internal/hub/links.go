// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
	corenames "github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// A project is a registration: a link dials the hub, proves a key, and is held until a person decides about it. The key is how the hub
// knows it from then on. Only then is anything it says listened to, and the hub asks it nothing before. What a link says is
// untrusted: it speaks for itself only, in names, sizes and rates the hub bounds (clean.go).

// OfflineError is returned for what needs a project whose link is not connected.
type OfflineError struct{ Since time.Time }

func (e OfflineError) Error() string {
	if e.Since.IsZero() {
		return "the project is offline"
	}
	return "the project is offline since " + e.Since.Format(time.RFC3339)
}

// ErrBadHello is returned for a link whose name the hub would not show.
var ErrBadHello = errors.New("the link does not name a project egzo could make")

// connection is one link, for as long as it stays connected.
type connection struct {
	project string
	link    links.Conn
	hello   links.Hello
	address string // where a person reaches the hub, for the address of a pending project
	ctx     context.Context
	end     context.CancelFunc // the connection, or the approval it ran under, has ended
	served  atomic.Bool        // the project is approved: what the link pushes is listened to
	tell    sync.Mutex         // one verdict at a time, the last one being the latest decision
	pace    pace
}

// gone reports whether nothing can reach the link any more.
func (c *connection) gone() bool {
	select {
	case <-c.link.Closed():
		return true
	case <-c.ctx.Done():
		return true
	default:
		return false
	}
}

// Link holds a link that proved its key until the connection ends. A key the hub does not know becomes a pending project, and a second
// connection with the same key replaces the first: it is the same link, come back before the first was noticed gone. address is where
// a person reaches the hub.
func (h *Hub) Link(ctx context.Context, hello links.Hello, conn links.Conn, address string) error {
	hello = cleanHello(hello)
	if !corenames.Project(hello.Project) {
		return ErrBadHello
	}
	reg, err := h.store.RegistrationByKey(ctx, hello.Key)
	if errors.Is(err, store.ErrNotFound) {
		reg, err = h.store.Register(ctx, store.Registration{Key: hello.Key, Name: hello.Project, Host: hello.Host, Release: hello.Release, Templates: hello.Templates})
		if err == nil {
			h.log.Info("a project asks to link", "project", reg.Name, "host", reg.Host, "id", reg.ID)
		}
	}
	if err != nil {
		return err
	}
	running, end := context.WithCancel(context.WithoutCancel(ctx))
	defer end()
	c := &connection{project: reg.ID, link: conn, hello: hello, address: address, ctx: running, end: end}
	h.mu.Lock()
	previous := h.conns[reg.ID]
	h.conns[reg.ID] = c
	h.mu.Unlock()
	if previous != nil {
		previous.end()
		_ = previous.link.Close()
	}
	defer h.disconnected(c)
	if err := h.store.Connected(ctx, reg.ID, hello.Project, hello.Host, hello.Release); err != nil {
		return err
	}
	if reg.State == hublink.StateApproved {
		h.serve(c)
	}
	if err := h.tell(ctx, c); err != nil {
		return err
	}
	return conn.Run(ctx, h.handlers(c))
}

// serve starts listening to a connection. The followers exist before the link hears it is approved: a feed asks them where to begin.
func (h *Hub) serve(c *connection) {
	h.feeds.connected(c)
	c.served.Store(true)
}

// tell sends the link where its registration stands now. Reading it under the connection's lock makes the last verdict sent the latest
// decision, whatever the order of a link that arrives and a person who approves it.
func (h *Hub) tell(ctx context.Context, c *connection) error {
	c.tell.Lock()
	defer c.tell.Unlock()
	reg, err := h.store.Registration(ctx, c.project)
	if err != nil {
		return err
	}
	return c.link.Send(h.verdict(ctx, reg, c.address))
}

func (h *Hub) verdict(ctx context.Context, reg store.Registration, address string) links.Verdict {
	v := links.Verdict{State: reg.State, Project: reg.ID, Message: reg.Message}
	switch reg.State {
	case hublink.StatePending:
		v.Code, v.Address = reg.Code, address+"/link/"+reg.Code
	case hublink.StateApproved:
		if owner, err := h.store.User(ctx, reg.Owner); err == nil {
			v.Owner = owner.Username
		}
	}
	return v
}

// disconnected is the end of a connection. A newer one of the same project leaves things as they are.
func (h *Hub) disconnected(c *connection) {
	c.end()
	_ = c.link.Close()
	h.mu.Lock()
	current := h.conns[c.project] == c
	if current {
		delete(h.conns, c.project)
	}
	h.mu.Unlock()
	if !current {
		return
	}
	h.feeds.disconnected(c)
	if err := h.store.Disconnected(context.Background(), c.project, time.Now()); err != nil {
		h.log.Warn("record that a link went away", "project", c.project, "err", err)
	}
}

// handlers is what the hub does with what a link pushes. Nothing is listened to unless the project is approved, and a link that pushes
// fast is held back: its stream fills, and it waits.
func (h *Hub) handlers(c *connection) links.Handlers {
	return links.Handlers{
		Since: func() time.Time { return h.feeds.since(c.project) },
		Project: func(update links.ProjectUpdate) {
			if c.served.Load() {
				c.pace.wait()
				h.pushed(c, update)
			}
		},
		Status: func(line links.StatusLine) {
			if c.served.Load() {
				c.pace.wait()
				h.feeds.status(c.project, cleanStatus(line))
			}
		},
		Audit: func(event links.AuditEvent) {
			if c.served.Load() {
				c.pace.wait()
				h.feeds.handleAudit(c.project, event)
			}
		},
		Unlink: func() links.Verdict {
			h.unlinked(c)
			return links.Verdict{State: hublink.StateUnlinked}
		},
	}
}

// heldProject is what a link last pushed, which is what the pages show: they ask the link nothing.
type heldProject struct {
	project links.Project
	known   bool
}

// pushed keeps a project as its link says it is now.
func (h *Hub) pushed(c *connection, update links.ProjectUpdate) {
	project := withTemplates(cleanProject(update.Project), update.Published)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held[c.project] = &heldProject{project: project, known: true}
}

// unlinked is a link asking to be forgotten: a project is unlinked from its own file.
func (h *Hub) unlinked(c *connection) {
	ctx := context.Background()
	reg, err := h.store.Registration(ctx, c.project)
	if err != nil {
		return
	}
	h.forget(ctx, reg, "project.unlink", "link")
}

// forget drops a project from the database and from memory.
func (h *Hub) forget(ctx context.Context, reg store.Registration, action, actor string) {
	if err := h.store.ForgetProject(ctx, reg.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.log.Warn("forget a project", "project", reg.ID, "err", err)
	}
	h.mu.Lock()
	delete(h.held, reg.ID)
	h.mu.Unlock()
	h.feeds.forget(reg.ID)
	h.forgetTasks(reg.ID)
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: actor, Action: action, Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
}

// --- what a person decides -------------------------------------------------------------------------------------------------------

func (h *Hub) requireOperator(ctx context.Context, user store.User, action, project string) error {
	if user.IsOperator {
		return nil
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: action, Project: project, Detail: "refused: only an operator may do that"})
	return ErrForbidden
}

// Pending reads the project that waits under a device code, for the page that approves it. Only an operator may: to anyone else the
// address leads nowhere.
func (h *Hub) Pending(ctx context.Context, user store.User, code string) (store.Registration, error) {
	if err := h.requireOperator(ctx, user, "project.read", ""); err != nil {
		return store.Registration{}, err
	}
	reg, err := h.store.RegistrationByCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) {
		return reg, ErrNotFound
	}
	return reg, err
}

// Approve links the project that waits under a device code. Whoever approves it owns it. Only an operator may, until the invites of
// roadmap/hub-link.md say who else can create a project.
func (h *Hub) Approve(ctx context.Context, user store.User, code string) (store.Registration, error) {
	if err := h.requireOperator(ctx, user, "project.approve", ""); err != nil {
		return store.Registration{}, err
	}
	reg, err := h.store.Approve(ctx, code, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		return reg, ErrNotFound
	}
	if err != nil {
		return reg, err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "project.approve", Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
	if c := h.connected(reg.ID); c != nil {
		h.serve(c)
		if err := h.tell(ctx, c); err != nil {
			h.log.Warn("tell a link it is approved", "project", reg.ID, "err", err)
		}
	}
	return reg, nil
}

// Refuse turns away the project that waits under a device code. Its link stays connected, to say so: `egzo ps` shows it. Only an
// operator may.
func (h *Hub) Refuse(ctx context.Context, user store.User, code, why string) error {
	if err := h.requireOperator(ctx, user, "project.refuse", ""); err != nil {
		return err
	}
	reg, err := h.store.Refuse(ctx, code, termsafe.Truncate(termsafe.Line(why), maxMessage))
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "project.refuse", Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
	h.told(ctx, reg.ID)
	return nil
}

// Revoke takes a project off the hub: it is gone from its pages, what runs over its link ends, and its link is refused from now on. The
// project keeps running on its machine. Only an operator may.
func (h *Hub) Revoke(ctx context.Context, user store.User, id, why string) error {
	if err := h.requireOperator(ctx, user, "project.revoke", id); err != nil {
		return err
	}
	reg, err := h.store.Revoke(ctx, id, termsafe.Truncate(termsafe.Line(why), maxMessage))
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "project.revoke", Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
	h.mu.Lock()
	delete(h.held, reg.ID)
	h.mu.Unlock()
	h.told(ctx, reg.ID)
	return nil
}

// told stops listening to a project that was turned away and tells its link, which stays connected to say so.
func (h *Hub) told(ctx context.Context, id string) {
	c := h.connected(id)
	if c == nil {
		return
	}
	if c.served.Swap(false) {
		h.feeds.disconnected(c)
	}
	if err := h.tell(ctx, c); err != nil {
		h.log.Warn("tell a link it was turned away", "project", id, "err", err)
	}
	c.end() // what runs over it ends here, whatever the link does: a terminal, a verb
}

// Forget removes a project that was refused or revoked, for a link that was turned away by mistake: it can ask again, and it is a new
// project. Only an operator may. One that is linked is revoked first.
func (h *Hub) Forget(ctx context.Context, user store.User, id string) error {
	if err := h.requireOperator(ctx, user, "project.forget", id); err != nil {
		return err
	}
	reg, err := h.store.Registration(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if reg.State != hublink.StateRefused && reg.State != hublink.StateRevoked {
		return ErrNotTurnedAway
	}
	h.forget(ctx, reg, "project.forget", rbac.Actor(user.ID))
	if c := h.connected(id); c != nil {
		_ = c.link.Close() // it comes back as a new registration
	}
	return nil
}

// ErrNotTurnedAway is returned for a project that cannot be forgotten as it is.
var ErrNotTurnedAway = errors.New("only a project that was refused or revoked can be forgotten: revoke it first")

// LinkView is a registration as an operator sees it.
type LinkView struct {
	store.Registration
	OwnerName string
}

// Links lists every registration, whatever its state. Only an operator may.
func (h *Hub) Links(ctx context.Context, user store.User) ([]LinkView, error) {
	if err := h.requireOperator(ctx, user, "links.list", ""); err != nil {
		return nil, err
	}
	regs, err := h.store.Registrations(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]LinkView, 0, len(regs))
	for _, reg := range regs {
		view := LinkView{Registration: reg}
		if reg.Owner != "" {
			if owner, err := h.store.User(ctx, reg.Owner); err == nil {
				view.OwnerName = owner.Username
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (h *Hub) connected(id string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[id]
}

// --- what a link may ask of the hub's time ----------------------------------------------------------------------------------------

// perSecond is how many lines of its feeds a connection gets through in a second. A proxy's log of 2000 lines reads back in a second.
const perSecond = 5000

// pace holds back whoever goes faster than perSecond: what it waits in is the feed's own goroutine.
type pace struct {
	mu   sync.Mutex
	next time.Time
}

func (p *pace) wait() {
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now.Add(-time.Second)) {
		p.next = now.Add(-time.Second) // a quiet link has a second's worth to spend at once
	}
	p.next = p.next.Add(time.Second / perSecond)
	wait := p.next.Sub(now)
	p.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}
