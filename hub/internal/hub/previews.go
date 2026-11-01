// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/preview"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// exposedPort is what the status feed says of one exposed port.
type exposedPort struct {
	agent string
	links.Exposure
}

// exposedIn lists what a status line says is exposed, by id. A link speaks for its own project only: the host name of a preview holds the
// project, so an id in another project's list is another address.
func exposedIn(line links.StatusLine) map[string]exposedPort {
	exposed := map[string]exposedPort{}
	for _, a := range line.Agents {
		for _, e := range a.Exposed {
			if e.Where != hublink.WhereHub {
				continue // a port open for the local machine is never served as a preview
			}
			exposed[e.ID] = exposedPort{agent: a.Agent, Exposure: e}
		}
	}
	return exposed
}

func (m *feedManager) forgetExposed(project string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.exposed, project)
}

// ServePreviews says the hub serves previews under a domain: a project hears the address template from then on.
func (h *Hub) ServePreviews(scheme, domain string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.previews = previewSite{scheme: scheme, domain: domain}
}

type previewSite struct{ scheme, domain string }

// previewTemplate is what a project's addresses look like, "" when the hub serves no previews.
func (h *Hub) previewTemplate(project string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.previews.domain == "" {
		return ""
	}
	return preview.Template(h.previews.scheme, project, h.previews.domain)
}

// PreviewView is a port an agent has open, as a page shows it: Where says whether Address is a preview to open or a port on the project's machine.
type PreviewView struct {
	Port                           int
	Name, Protocol, Where, Address string
}

// previewsOf lists what an agent has open, by port. They are the project's to know, addresses included: an operator who holds no role sees them,
// and a preview asks them for a role when they open it. A local port's address is shown to all of them too. Hiding it would protect nothing, as the
// port is open to whoever reaches it either way, and it would hide from the people overseeing a project what it has opened.
func (h *Hub) previewsOf(project, agent string) []PreviewView {
	template := h.previewTemplate(project)
	h.feeds.mu.Lock()
	defer h.feeds.mu.Unlock()
	var views []PreviewView
	for id, port := range h.feeds.exposed[project] {
		if port.agent != agent {
			continue
		}
		if template == "" || port.Protocol != hublink.ProtocolHTTP {
			continue // nothing the hub can open
		}
		views = append(views, PreviewView{Port: port.Port, Name: port.Name, Protocol: port.Protocol, Where: hublink.WhereHub,
			Address: strings.Replace(template, "%s", id, 1)})
	}
	// What the latest status line says, and nothing once the feed has gone: an address is only worth showing while it may be true.
	if f := h.feeds.followers[project]; f != nil {
		for _, e := range f.agents[agent].Exposed {
			if e.Where != hublink.WhereLocal {
				continue
			}
			views = append(views, PreviewView{Port: e.Port, Name: e.Name, Protocol: e.Protocol, Where: e.Where, Address: e.Address})
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Port != views[j].Port {
			return views[i].Port < views[j].Port
		}
		return views[i].Protocol < views[j].Protocol
	})
	return views
}

// PreviewBackend is the hub as the preview proxy needs it.
type PreviewBackend struct{ hub *Hub }

// Previews gives the preview proxy what it needs of the hub.
func (h *Hub) Previews() PreviewBackend { return PreviewBackend{hub: h} }

// Find says where a preview is now. A project that is not connected says so to whoever may view it: its addresses stay its own.
func (b PreviewBackend) Find(ctx context.Context, project, id string) (preview.Target, error) {
	h := b.hub
	if _, err := h.registration(ctx, project); err != nil {
		return preview.Target{}, preview.ErrNotFound
	}
	if c := h.connected(project); c == nil || !c.served.Load() {
		return preview.Target{}, preview.ErrOffline
	}
	h.feeds.mu.Lock()
	port, ok := h.feeds.exposed[project][id]
	h.feeds.mu.Unlock()
	if !ok || port.Protocol != hublink.ProtocolHTTP {
		return preview.Target{}, preview.ErrNotFound
	}
	return preview.Target{Project: project, ID: id, Agent: port.agent, Port: port.Port}, nil
}

// Dial opens a connection to a preview. It lasts until it is closed or the link goes: what ended the connection to the project ends this too.
func (b PreviewBackend) Dial(ctx context.Context, target preview.Target) (net.Conn, error) {
	c := b.hub.connected(target.Project)
	if c == nil || !c.served.Load() {
		return nil, preview.ErrOffline
	}
	conn, err := c.link.DialPort(ctx, target.Agent, target.Port)
	if err != nil {
		if c.gone() {
			return nil, preview.ErrOffline
		}
		return nil, err
	}
	ended := &endingConn{Conn: conn}
	ended.stop = context.AfterFunc(c.ctx, func() { conn.Close() })
	return ended, nil
}

// endingConn stops watching the link when it closes.
type endingConn struct {
	net.Conn
	stop func() bool
	once sync.Once
}

func (c *endingConn) Close() error {
	c.once.Do(func() { c.stop() })
	return c.Conn.Close()
}

// CloseWrite passes a half close on, when the connection can do one.
func (c *endingConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errors.ErrUnsupported
}

// previewTicketLife is how long a redirect from the hub to a preview may take.
const previewTicketLife = 30 * time.Second

// mayPreview says whether a person may open a project's previews now: every role may, and only a role.
func (h *Hub) mayPreview(ctx context.Context, user store.User, project string) (rbac.Role, bool, error) {
	if _, err := h.registration(ctx, project); err != nil {
		return rbac.None, false, err
	}
	role, err := h.store.Role(ctx, project, user.ID)
	if err != nil {
		return rbac.None, false, err
	}
	return role, rbac.Can(user.Subject(), role, rbac.Preview, ""), nil
}

// OpenPreview lets a person into a preview, and returns the ticket that takes them there. Somebody who may not sees what a preview that does not
// exist shows: the address says nothing of the project. It is one entry in the audit log per ticket, not per request.
func (h *Hub) OpenPreview(ctx context.Context, user store.User, sessionHash, project, id, path string) (string, error) {
	role, allowed, err := h.mayPreview(ctx, user, project)
	if err != nil {
		return "", err
	}
	if !allowed {
		h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "preview.open", Project: project, Detail: fmt.Sprintf("refused: %s holds no role", role)})
		return "", ErrNotFound
	}
	agent, port := "", 0
	target, err := h.Previews().Find(ctx, project, id)
	switch {
	case err == nil:
		agent, port = target.Agent, target.Port
	case !errors.Is(err, preview.ErrOffline): // an offline project says so on the preview's own host
		return "", ErrNotFound
	}
	ticket, err := h.store.NewPreviewTicket(ctx, sessionHash, project, id, path, previewTicketLife)
	if err != nil {
		return "", err
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "preview.open", Project: project, Agent: agent, Detail: fmt.Sprintf("port %d", port), Allowed: true})
	return ticket, nil
}

// EnterPreview spends a ticket, on the preview's host, and returns the path to go to and the cookie's value.
func (h *Hub) EnterPreview(ctx context.Context, ticket, project, id string) (string, string, error) {
	path, token, err := h.store.SpendPreviewTicket(ctx, ticket, project, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", ErrNotFound
	}
	return path, token, err
}

// Visitor is a person a preview's cookie stands for.
type Visitor struct {
	User    store.User
	Session string // the hash of the hub session it came from
	Expires time.Time
}

// PreviewVisitor checks a preview's cookie, on each request: the session lives, the user is enabled, and they still hold a role.
func (h *Hub) PreviewVisitor(ctx context.Context, token, project, id string) (Visitor, error) {
	user, session, expires, err := h.store.PreviewVisitor(ctx, token, project, id)
	if errors.Is(err, store.ErrNotFound) {
		return Visitor{}, ErrNotFound
	}
	if err != nil {
		return Visitor{}, err
	}
	if _, allowed, err := h.mayPreview(ctx, user, project); err != nil {
		return Visitor{}, err
	} else if !allowed {
		return Visitor{}, ErrNotFound
	}
	return Visitor{User: user, Session: session, Expires: expires}, nil
}

// openPreview is a request a preview's server is answering. A WebSocket or an event stream asks nothing more, so what would refuse its next
// request ends it instead, as with a terminal.
type openPreview struct {
	project, id, user string
	cancel            func()
}

// TrackPreview registers a request; the returned func forgets it.
func (h *Hub) TrackPreview(project, id, userID string, cancel func()) func() {
	open := &openPreview{project: project, id: id, user: userID, cancel: cancel}
	h.previewMu.Lock()
	defer h.previewMu.Unlock()
	if h.previewing == nil {
		h.previewing = map[*openPreview]struct{}{}
	}
	h.previewing[open] = struct{}{}
	return func() {
		h.previewMu.Lock()
		defer h.previewMu.Unlock()
		delete(h.previewing, open)
	}
}

func (h *Hub) endPreviews(ended func(*openPreview) bool) {
	h.previewMu.Lock()
	defer h.previewMu.Unlock()
	for open := range h.previewing {
		if ended(open) {
			open.cancel()
		}
	}
}

// endLostPreviews ends what a person may no longer watch now that they have role on a project.
func (h *Hub) endLostPreviews(project, userID string, role rbac.Role) {
	if rbac.Can(rbac.Subject{ID: userID}, role, rbac.Preview, "") {
		return
	}
	h.endPreviews(func(open *openPreview) bool { return open.project == project && open.user == userID })
}

// endUnexposed ends what is open on a port that is no longer exposed.
func (h *Hub) endUnexposed(project string) {
	h.feeds.mu.Lock()
	exposed := h.feeds.exposed[project]
	h.feeds.mu.Unlock()
	h.endPreviews(func(open *openPreview) bool {
		_, ok := exposed[open.id]
		return open.project == project && !ok
	})
}
