package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

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

// ChatMessage is a message of a project as the hub shows it: in an agent's Rich UI chat, where egzo's line announcing it names it ("check
// egzo message m… and handle the request for me."), and in its activity.
type ChatMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`                // who sent it, in words: a person's name on the hub, an agent's, "operator" or "egzo"
	FromAgent bool      `json:"fromAgent,omitempty"` // an agent of the project sent it
	To        string    `json:"to"`                  // who it went to, in the same words
	ToAgent   bool      `json:"toAgent,omitempty"`
	Kind      string    `json:"kind"` // request, question, resolution, update
	Subject   string    `json:"subject"`
	Text      string    `json:"text"`
	Re        string    `json:"re,omitempty"` // what it answers or is part of: the id of the message
	Choices   []string  `json:"choices,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`  // a resolution's, or how a request or question was resolved
	Resolved  bool      `json:"resolved,omitempty"` // a request or question that was resolved
	Time      time.Time `json:"time"`
}

// ChatMessageOf is a message sent to an agent, for someone who may watch its chat: the hub keeps every message of the project from its
// events feed. ErrNotFound for a message the agent sent, one to another agent, or one the hub never had.
func (h *Hub) ChatMessageOf(ctx context.Context, user store.User, project, agent, id string) (ChatMessage, error) {
	if err := h.mayInspectAgent(ctx, user, project, agent); err != nil {
		return ChatMessage{}, err
	}
	m, err := h.store.MessageOf(ctx, project, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && m.To != "agent:"+agent {
		return ChatMessage{}, ErrNotFound
	}
	if err != nil {
		return ChatMessage{}, err
	}
	return h.chatMessage(ctx, m), nil
}

// AgentMessages lists what an agent sent and was sent, newest first, at most limit, before a time when it is not zero: its activity.
// Whoever may watch the agent's chat may read them.
func (h *Hub) AgentMessages(ctx context.Context, user store.User, project, agent string, before time.Time, limit int) ([]ChatMessage, error) {
	if err := h.mayInspectAgent(ctx, user, project, agent); err != nil {
		return nil, err
	}
	list, err := h.store.MessagesOf(ctx, project, "agent:"+agent, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ChatMessage, 0, len(list))
	for _, m := range list {
		out = append(out, h.chatMessage(ctx, m))
	}
	return out, nil
}

// mayInspectAgent is whether the user may see what an agent did: its chat, its activity.
func (h *Hub) mayInspectAgent(ctx context.Context, user store.User, project, agent string) error {
	role, err := h.access(ctx, user, project)
	if err != nil {
		return err
	}
	creator, _, err := h.agentOf(ctx, project, agent)
	if err != nil {
		return err
	}
	if !rbac.Can(user.Subject(), role, rbac.Inspect, creator) {
		return ErrForbidden
	}
	return nil
}

func (h *Hub) chatMessage(ctx context.Context, m store.Message) ChatMessage {
	c := ChatMessage{ID: m.ID, Kind: m.Kind, Subject: m.Subject, Text: m.Text, Re: m.Re, Choices: m.Choices, Outcome: m.Outcome,
		Resolved: !m.ResolvedAt.IsZero(), Time: m.At}
	c.From, c.FromAgent = h.addressName(ctx, m.From)
	c.To, c.ToAgent = h.addressName(ctx, m.To)
	return c
}

// addressName is an address in words: a person's name on the hub ("someone" for one it does not know), an agent's name, or operator or
// egzo as they are; and whether it is an agent.
func (h *Hub) addressName(ctx context.Context, address string) (string, bool) {
	if name, ok := strings.CutPrefix(address, "agent:"); ok {
		return name, true
	}
	if id, ok := strings.CutPrefix(address, "user:"); ok {
		if u, err := h.store.User(ctx, id); err == nil {
			return cmp.Or(u.Name, u.Username), false
		}
		return "someone", false
	}
	return address, false
}
