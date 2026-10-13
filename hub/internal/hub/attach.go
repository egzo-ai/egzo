package hub

import (
	"context"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Viewer is somebody attached to an agent's terminal.
type Viewer struct {
	UserID, Name string
	ReadOnly     bool
}

func attachKey(engine, project, agent string) string { return engine + ":" + project + "/" + agent }

// presence says who holds an agent's terminal and how many watch it.
func (h *Hub) presence(engine, project, agent string) (writer string, viewers int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, v := range h.attached[attachKey(engine, project, agent)] {
		if v.ReadOnly {
			viewers++
		} else {
			writer = v.Name
		}
	}
	return writer, viewers
}

// Viewers lists who is attached to an agent.
func (h *Hub) Viewers(ctx context.Context, user store.User, engine, project, agent string) ([]Viewer, error) {
	if _, err := h.access(ctx, user, engine, project); err != nil {
		return nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []Viewer
	for _, v := range h.attached[attachKey(engine, project, agent)] {
		out = append(out, *v)
	}
	return out, nil
}

// Attachment is a terminal the hub agreed to open.
type Attachment struct {
	// ReadOnly is true when the user may only watch: they may not operate the agent, or somebody else already holds the keyboard.
	ReadOnly bool

	hub                    *Hub
	engine, project, agent string
	viewer                 *Viewer
}

// Attach decides whether the user writes or watches (one writer per agent, anybody with a role on the project may watch), registers them,
// and returns the attachment to run. Close it when the terminal ends.
func (h *Hub) Attach(ctx context.Context, user store.User, engine, project, agent string) (*Attachment, error) {
	role, err := h.access(ctx, user, engine, project)
	if err != nil {
		return nil, err
	}
	creator, err := h.creatorOf(ctx, engine, project, agent)
	if err != nil {
		return nil, err
	}
	canWrite := rbac.Can(user.Subject(), role, rbac.Operate, creator)

	h.mu.Lock()
	key := attachKey(engine, project, agent)
	readOnly := true
	if canWrite {
		readOnly = false
		for _, v := range h.attached[key] {
			if !v.ReadOnly {
				readOnly = true
			}
		}
	}
	viewer := &Viewer{UserID: user.ID, Name: user.DisplayName(), ReadOnly: readOnly}
	h.attached[key] = append(h.attached[key], viewer)
	h.mu.Unlock()

	detail := "write"
	switch {
	case readOnly && canWrite:
		detail = "read-only: another user holds the terminal"
	case readOnly:
		detail = "read-only"
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.attach", Engine: engine, Project: project, Agent: agent, Detail: detail, Allowed: true})
	return &Attachment{ReadOnly: readOnly, hub: h, engine: engine, project: project, agent: agent, viewer: viewer}, nil
}

// Run connects the terminal until it ends.
func (a *Attachment) Run(ctx context.Context, term engines.Terminal) error {
	term.ReadOnly = a.ReadOnly
	return a.hub.call(ctx, a.engine, func(e engines.Engine) error { return e.Attach(ctx, a.project, a.agent, term) })
}

// Close frees the seat.
func (a *Attachment) Close() {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	key := attachKey(a.engine, a.project, a.agent)
	list := a.hub.attached[key]
	for i, v := range list {
		if v == a.viewer {
			a.hub.attached[key] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(a.hub.attached[key]) == 0 {
		delete(a.hub.attached, key)
	}
}
