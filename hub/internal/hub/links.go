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

// A project is a registration: an operator makes a link code, and a link dials the hub, proves a key and presents the code. The key is how
// the hub knows it from then on. A link with neither a known key nor a good code hears that it is unregistered and is closed, and leaves
// nothing behind: the hub stores nothing for a stranger, and holds no connection. What an approved link says is untrusted too: it speaks
// for itself only, in names, sizes and rates the hub bounds (clean.go).

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

// ErrUnregistered is returned for a link the hub does not know, which presented no code. ErrBadCode is the same for one whose code is no
// good. The link hears the same from both, and nothing of why a code failed.
var (
	ErrUnregistered = errors.New("the link's key is not registered")
	ErrBadCode      = errors.New("the link's code is no good")
)

// connection is one link, for as long as it stays connected.
type connection struct {
	project string
	link    links.Conn
	hello   links.Hello
	ctx     context.Context
	end     context.CancelFunc // the connection, or the approval it ran under, has ended
	served  atomic.Bool        // published, and not turned away: the link is told approved, and what it pushes is listened to
	tell    sync.Mutex         // one verdict at a time, the last one being the latest decision
	lines   pace
	bytes   pace
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

// Link takes a link that proved its key. An approved project is held until the connection ends, and a second connection with the same key
// replaces the first: it is the same link, come back before the first was noticed gone. A key the hub does not know registers with a good
// code. Anything else hears where it stands and is closed: nothing but an approved project reaches Run, and a stranger costs one read.
func (h *Hub) Link(ctx context.Context, hello links.Hello, code string, conn links.Conn) error {
	defer conn.Close()
	hello = cleanHello(hello)
	if !corenames.Project(hello.Project) {
		return ErrBadHello
	}
	reg, err := h.store.RegistrationByKey(ctx, hello.Key)
	if errors.Is(err, store.ErrNotFound) {
		reg, err = h.register(ctx, hello, code)
	}
	if errors.Is(err, ErrUnregistered) || errors.Is(err, ErrBadCode) {
		_ = conn.Send(links.Verdict{State: hublink.StateUnregistered})
		return err
	}
	if err != nil {
		return err
	}
	if reg.State != hublink.StateApproved {
		return conn.Send(h.verdict(ctx, reg))
	}
	return h.hold(ctx, reg.ID, hello, conn)
}

// hold serves an approved project until the connection ends.
func (h *Hub) hold(ctx context.Context, id string, hello links.Hello, conn links.Conn) error {
	running, end := context.WithCancel(context.WithoutCancel(ctx))
	defer end()
	c := &connection{project: id, link: conn, hello: hello, ctx: running, end: end}
	defer h.disconnected(c)
	if err := h.arrived(ctx, c); err != nil {
		return err
	}
	// Read again now the connection is published: a Revoke before this line is seen here, one after it finds the connection.
	reg, err := h.tell(ctx, c)
	if err != nil {
		return err
	}
	if reg.State != hublink.StateApproved {
		return nil
	}
	return conn.Run(ctx, h.handlers(c))
}

// register spends a link code on a new key. Two connections of one key may race: the second finds the project the first made, and leaves
// its code unspent.
func (h *Hub) register(ctx context.Context, hello links.Hello, code string) (store.Registration, error) {
	if code == "" {
		return store.Registration{}, ErrUnregistered
	}
	reg, err := h.store.RedeemLinkCode(ctx, code, store.Registration{Key: hello.Key, Name: hello.Project, Host: hello.Host, Release: hello.Release, Templates: hello.Templates}, time.Now())
	switch {
	case errors.Is(err, store.ErrKeyTaken):
		return h.store.RegistrationByKey(ctx, hello.Key)
	case errors.Is(err, store.ErrBadCode):
		return reg, ErrBadCode
	case err != nil:
		return reg, err
	}
	h.log.Info("a project registered", "project", reg.Name, "host", reg.Host, "id", reg.ID)
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(reg.Owner), Action: "project.register", Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
	return reg, nil
}

// arrived publishes a connection and readies what follows it. The followers exist before the link hears it is approved: a feed asks them
// where to begin. Under h.linking, so that the end of the connection before this one cannot undo any of it.
func (h *Hub) arrived(ctx context.Context, c *connection) error {
	h.linking.Lock()
	h.mu.Lock()
	previous := h.conns[c.project]
	h.conns[c.project] = c
	h.mu.Unlock()
	if previous != nil {
		previous.end()
	}
	err := h.store.Connected(ctx, c.project, c.hello.Project, c.hello.Host, c.hello.Release)
	if err == nil {
		h.feeds.connected(c)
		c.served.Store(true)
	}
	h.linking.Unlock()
	if previous != nil {
		_ = previous.link.Close()
	}
	return err
}

// tell sends the link where its registration stands now, and says what it said. Reading it under the connection's lock makes the last
// verdict sent the latest decision, whatever the order of a link that arrives and a person who revokes it.
func (h *Hub) tell(ctx context.Context, c *connection) (store.Registration, error) {
	c.tell.Lock()
	defer c.tell.Unlock()
	reg, err := h.store.Registration(ctx, c.project)
	if err != nil {
		return reg, err
	}
	return reg, c.link.Send(h.verdict(ctx, reg))
}

func (h *Hub) verdict(ctx context.Context, reg store.Registration) links.Verdict {
	v := links.Verdict{State: reg.State, Project: reg.ID, Message: reg.Message}
	if reg.State == hublink.StateApproved {
		if owner, err := h.store.User(ctx, reg.Owner); err == nil {
			v.Owner = owner.Username
		}
	}
	return v
}

// disconnected is the end of a connection. A newer one of the same project leaves things as they are: the check and what it guards are one
// step under h.linking, or a link that came back in between would lose its followers and be marked offline.
func (h *Hub) disconnected(c *connection) {
	c.end()
	_ = c.link.Close()
	h.linking.Lock()
	defer h.linking.Unlock()
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

// handlers is what the hub does with what a link pushes. Nothing is listened to once the project is turned away, and a link that pushes
// fast is held back, in bytes as they are read and in lines as they are decoded: its stream fills, and it waits.
func (h *Hub) handlers(c *connection) links.Handlers {
	return links.Handlers{
		Since: func() time.Time { return h.feeds.since(c.project) },
		Project: func(update links.ProjectUpdate) {
			if c.served.Load() {
				c.lines.wait(time.Second / linesPerSecond)
				h.pushed(c, update)
			}
		},
		Status: func(line links.StatusLine) {
			if c.served.Load() {
				c.lines.wait(time.Second / linesPerSecond)
				h.feeds.status(c.project, cleanStatus(line))
			}
		},
		Audit: func(event links.AuditEvent) {
			if c.served.Load() {
				c.lines.wait(time.Second / linesPerSecond)
				h.feeds.handleAudit(c.project, event)
			}
		},
		Unlink: func() links.Verdict { return h.unlinked(c) },
		Pace:   func(n int) { c.bytes.wait(time.Duration(n) * time.Second / bytesPerSecond) },
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
	if c.served.Load() { // under the lock: what forgets a project stops listening first, and deletes this under the same lock
		h.held[c.project] = &heldProject{project: project, known: true}
	}
}

// unlinked is a link asking to be forgotten: a project is unlinked from its own file. Only an approved project can ask: one that was revoked
// in the meantime keeps its record, and that is what the link hears.
func (h *Hub) unlinked(c *connection) links.Verdict {
	ctx := context.Background()
	c.served.Store(false)
	reg, err := h.store.Registration(ctx, c.project)
	if errors.Is(err, store.ErrNotFound) {
		return links.Verdict{State: hublink.StateUnlinked}
	}
	if err == nil {
		err = h.forget(ctx, reg, hublink.StateApproved, "project.unlink", "link")
	}
	if err == nil {
		return links.Verdict{State: hublink.StateUnlinked}
	}
	if !errors.Is(err, store.ErrNotFound) {
		h.log.Warn("forget a project", "project", c.project, "err", err)
	}
	if reg, err = h.store.Registration(ctx, c.project); err != nil {
		return links.Verdict{State: hublink.StateUnlinked}
	}
	return h.verdict(ctx, reg)
}

// forget drops a project that is in state from the database and from memory.
func (h *Hub) forget(ctx context.Context, reg store.Registration, state, action, actor string) error {
	if err := h.store.ForgetProject(ctx, reg.ID, state); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.held, reg.ID)
	h.mu.Unlock()
	h.feeds.forget(reg.ID)
	h.forgetTasks(reg.ID)
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: actor, Action: action, Project: reg.ID, Detail: reg.Name + " on " + reg.Host, Allowed: true})
	return nil
}

// --- what a person decides -------------------------------------------------------------------------------------------------------

func (h *Hub) requireOperator(ctx context.Context, user store.User, action, project string) error {
	if user.IsOperator {
		return nil
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: action, Project: project, Detail: "refused: only an operator may do that"})
	return ErrForbidden
}

// LinkCodeView is a code as an operator sees it, without the code.
type LinkCodeView struct {
	store.LinkCode
	CreatorName string
}

// CreateLinkCode makes a code that lets one project register, owned by whoever made it. What it returns is the only time the code is
// seen. Only an operator may, until the invites of roadmap/hub-link.md say who else can create a project.
func (h *Hub) CreateLinkCode(ctx context.Context, user store.User) (store.LinkCode, string, error) {
	if err := h.requireOperator(ctx, user, "linkcode.create", ""); err != nil {
		return store.LinkCode{}, "", err
	}
	made, code, err := h.store.CreateLinkCode(ctx, user.ID, time.Now())
	if err != nil {
		return store.LinkCode{}, "", err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "linkcode.create", Detail: made.ID, Allowed: true})
	return made, code, nil
}

// LinkCodes lists the codes that are still good. Only an operator may.
func (h *Hub) LinkCodes(ctx context.Context, user store.User) ([]LinkCodeView, error) {
	if err := h.requireOperator(ctx, user, "linkcode.list", ""); err != nil {
		return nil, err
	}
	codes, err := h.store.LinkCodes(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	views := make([]LinkCodeView, 0, len(codes))
	for _, code := range codes {
		view := LinkCodeView{LinkCode: code}
		if creator, err := h.store.User(ctx, code.CreatedBy); err == nil {
			view.CreatorName = creator.Username
		}
		views = append(views, view)
	}
	return views, nil
}

// CancelLinkCode takes a code back before it is used. Only an operator may.
func (h *Hub) CancelLinkCode(ctx context.Context, user store.User, id string) error {
	if err := h.requireOperator(ctx, user, "linkcode.cancel", ""); err != nil {
		return err
	}
	if err := h.store.CancelLinkCode(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "linkcode.cancel", Detail: id, Allowed: true})
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
	if c := h.connected(reg.ID); c != nil {
		c.served.Store(false)
		if _, err := h.tell(ctx, c); err != nil {
			h.log.Warn("tell a link it was revoked", "project", reg.ID, "err", err)
		}
		c.end() // what runs over it ends here, whatever the link does: a terminal, a verb
		go c.link.Finish()
	}
	h.mu.Lock()
	delete(h.held, reg.ID)
	h.mu.Unlock()
	return nil
}

// Forget removes a project that was revoked, for a link that was turned away by mistake: a new code brings it back, as a new project with no
// roles. Only an operator may. One that is linked is revoked first.
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
	if reg.State != hublink.StateRevoked {
		return ErrNotTurnedAway
	}
	if err := h.forget(ctx, reg, hublink.StateRevoked, "project.forget", rbac.Actor(user.ID)); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// ErrNotTurnedAway is returned for a project that cannot be forgotten as it is.
var ErrNotTurnedAway = errors.New("only a project that was revoked can be forgotten: revoke it first")

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

const (
	// linesPerSecond is how many lines of its feeds a connection gets through in a second. A proxy's log of 2000 lines reads back in a second.
	linesPerSecond = 5000
	// bytesPerSecond is how many bytes of its feeds a connection gets through in a second: the longest line a link may send. It was worked out,
	// not measured on real traffic: a proxy's whole backlog of lines as long as they get is about one second of it
	// (TestABacklogOfTheProxysLogFitsTheBudget), so it holds back a link that floods, and nothing a project does.
	bytesPerSecond = hublink.MaxLine
)

// pace holds back whoever spends faster than time passes: what it waits in is the feed's own goroutine.
type pace struct {
	mu   sync.Mutex
	next time.Time
}

// wait spends cost of the connection's time. It takes a duration, not a count, so that one pace can be in lines and another in bytes.
func (p *pace) wait(cost time.Duration) {
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now.Add(-time.Second)) {
		p.next = now.Add(-time.Second) // a quiet link has a second's worth to spend at once
	}
	p.next = p.next.Add(cost)
	wait := p.next.Sub(now)
	p.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}
