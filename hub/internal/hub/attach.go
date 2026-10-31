package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// InUseError is returned when somebody else holds the agent's terminal and the user did not ask to take it over.
type InUseError struct{ Holder string }

func (e *InUseError) Error() string { return fmt.Sprintf("the terminal is in use by %s", e.Holder) }

func attachKey(project, agent string) string { return project + "/" + agent }

// holder says who holds an agent's terminal, "" when nobody. Only the hub's own attaches count: a terminal opened with the CLI is not
// seen (docs/README.md, "Known issues").
func (h *Hub) holder(project, agent string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if a := h.attached[attachKey(project, agent)]; a != nil {
		return a.name
	}
	return ""
}

// Holder says who holds an agent's terminal, "" when nobody.
func (h *Hub) Holder(ctx context.Context, user store.User, project, agent string) (string, error) {
	if _, err := h.access(ctx, user, project); err != nil {
		return "", err
	}
	return h.holder(project, agent), nil
}

// Attachment is a terminal the hub agreed to open.
type Attachment struct {
	hub            *Hub
	project, agent string
	name           string
	taken          chan struct{}
	takenBy        string
}

// Taken is closed when somebody else took the terminal over.
func (a *Attachment) Taken() <-chan struct{} { return a.taken }

// TakenBy is who took the terminal over; it is only set once Taken is closed.
func (a *Attachment) TakenBy() string {
	a.hub.mu.RLock()
	defer a.hub.mu.RUnlock()
	return a.takenBy
}

// Attach gives the user the agent's terminal. One person holds it at a time: when somebody else does, it fails with an InUseError, unless
// takeOver is set, which ends the other person's terminal. Whoever may operate the agent may attach. Close the attachment when the terminal ends.
func (h *Hub) Attach(ctx context.Context, user store.User, project, agent string, takeOver bool) (*Attachment, error) {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return nil, err
	}
	creator, err := h.creatorOf(ctx, project, agent)
	if err != nil {
		return nil, err
	}
	if !rbac.Can(user.Subject(), role, rbac.Operate, creator) {
		return nil, ErrForbidden
	}

	att := &Attachment{hub: h, project: project, agent: agent, name: user.DisplayName(), taken: make(chan struct{})}
	key := attachKey(project, agent)
	h.mu.Lock()
	previous := h.attached[key]
	if previous != nil && !takeOver {
		h.mu.Unlock()
		return nil, &InUseError{Holder: previous.name}
	}
	if previous != nil {
		previous.takenBy = att.name
		close(previous.taken)
	}
	h.attached[key] = att
	h.mu.Unlock()

	detail := "open"
	if previous != nil {
		detail = "took over from " + previous.name
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.attach", Project: project, Agent: agent, Detail: detail, Allowed: true})
	return att, nil
}

// Run connects the terminal until it ends or is taken over. A taken-over terminal ends without an error: check Taken.
func (a *Attachment) Run(ctx context.Context, term links.Terminal) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-a.taken:
			cancel()
		case <-ctx.Done():
		}
	}()
	err := a.hub.call(ctx, a.project, func(ctx context.Context, e links.Engine) error { return e.Attach(ctx, a.agent, term) })
	select {
	case <-a.taken:
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	default:
	}
	return err
}

// Close frees the seat, unless somebody took it over already.
func (a *Attachment) Close() {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	key := attachKey(a.project, a.agent)
	if a.hub.attached[key] == a {
		delete(a.hub.attached, key)
	}
}
