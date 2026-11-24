package hub

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// ErrNotAHP is returned for the chat of an agent that was not spawned in AHP mode: it has a terminal and nothing else.
var ErrNotAHP = errors.New("this agent was not spawned with Rich UI: it has a terminal only")

// RichChat is the chat of an agent spawned in AHP mode (the Rich UI), as the hub agreed to open it for one person. ReadOnly says they
// watch it: they see the chat but cannot send a prompt or stop a turn.
type RichChat struct {
	hub            *Hub
	project, agent string
	ReadOnly       bool
}

// OpenChat opens the Rich UI chat of an agent for a user. Whoever may see what the project's agents did (Inspect) may watch it; whoever
// may operate the agent may also act in it, as for its terminal. Several people can have it open at once: it is not a seat.
func (h *Hub) OpenChat(ctx context.Context, user store.User, project, agent string) (*RichChat, error) {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return nil, err
	}
	creator, ahp, err := h.agentOf(ctx, project, agent)
	if err != nil {
		return nil, err
	}
	if !rbac.Can(user.Subject(), role, rbac.Inspect, creator) {
		h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.chat", Project: project, Agent: agent,
			Detail: fmt.Sprintf("refused: %s needs more than %s", rbac.Inspect, role)})
		return nil, ErrForbidden
	}
	if !ahp {
		return nil, ErrNotAHP
	}
	readOnly := !rbac.Can(user.Subject(), role, rbac.Operate, creator)
	detail := "open"
	if readOnly {
		detail = "open, read only"
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.chat", Project: project, Agent: agent, Detail: detail, Allowed: true})
	return &RichChat{hub: h, project: project, agent: agent, ReadOnly: readOnly}, nil
}

// Run connects to the agent's chat through its link and gives the connection to use, which carries lines of JSON both ways, until use
// returns or ctx ends.
func (c *RichChat) Run(ctx context.Context, use func(conn net.Conn) error) error {
	return c.hub.call(ctx, c.project, func(ctx context.Context, e links.Engine) error {
		conn, err := e.DialAHP(ctx, c.agent, c.ReadOnly)
		if err != nil {
			return err
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		return use(conn)
	})
}

// agentOf reads who started an agent, and whether it was spawned in AHP mode.
func (h *Hub) agentOf(ctx context.Context, id, agent string) (creator string, ahp bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err = h.call(ctx, id, func(ctx context.Context, e links.Engine) error {
		p, err := e.Project(ctx)
		if err != nil {
			return err
		}
		for _, a := range p.Agents {
			if a.Name == agent {
				creator, ahp = a.Actor, a.AHP
				return nil
			}
		}
		return ErrNotFound
	})
	if errors.As(err, new(links.ErrNoProject)) {
		return "", false, ErrNotFound
	}
	return creator, ahp, err
}
