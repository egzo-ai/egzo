// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/preview"
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
