// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Chat is what the hub does for the chat bridges (docs/hub-bridges.md): a user talks to an agent, and answers what the agent asks them,
// through a bridge. The hub is the broker: what agents say to users waits in the outbox, which bridges read from where they are. The hub is the second check on every message: the bridge vetted the platform's side, and the hub checks the person.
// A message it refuses is dropped and audited, and nobody is told: the bridge gets an error, never something to show.

// Inbound is a message for an agent from a person on a chat platform, as a bridge hands it over. Via names the bridge, and Account
// the person's chat account there: the hub knows whose it is (bridgeaccounts.go), and a bridge never names a hub user.
type Inbound struct {
	Via       string
	Account   string
	Text      string
	Key       string // the platform's id for the message: the same key is the same message
	Interrupt bool
	// Answer is the id of the question, or request, this answers; empty for a new message.
	Answer  string
	Outcome string // with Answer: done, declined or failed; "" is done
}

// Deliver checks an inbound message and hands it to the agent's project. It returns the id of the message the agent gets; an answer has
// none.
func (h *Hub) Deliver(ctx context.Context, project, agent string, in Inbound) (string, error) {
	action := "agent.message"
	if in.Answer != "" {
		action = "agent.answer"
	}
	actor := in.Via + ":" + in.Account // until the account is known to be someone's
	refuse := func(err error, why string) (string, error) {
		h.record(ctx, store.AuditEntry{Actor: actor, Action: action, Project: project, Agent: agent, Detail: fmt.Sprintf("refused, via %s: %s", in.Via, why)})
		return "", err
	}
	account, err := h.store.BridgeAccountOf(ctx, in.Via, in.Account)
	if err != nil {
		return refuse(ErrForbidden, "a chat account nobody linked")
	}
	user, err := h.store.User(ctx, account.UserID)
	if err != nil {
		return refuse(ErrForbidden, "no such user")
	}
	actor = rbac.Actor(user.ID)
	if user.Disabled() {
		return refuse(ErrForbidden, "the user is disabled")
	}
	role, err := h.access(ctx, user, project)
	if err != nil {
		return refuse(err, "no role on the project")
	}
	creator, err := h.creatorOf(ctx, project, agent)
	if err != nil {
		return refuse(err, "no such agent, or the project is offline")
	}
	if !rbac.Can(user.Subject(), role, rbac.Chat, creator) {
		return refuse(ErrForbidden, fmt.Sprintf("%s needs more than %s", rbac.Chat, role))
	}
	var id string
	err = h.call(ctx, project, func(ctx context.Context, e links.Engine) error {
		if in.Answer != "" {
			return e.Resolve(ctx, in.Answer, rbac.Actor(user.ID), in.Text, in.Outcome)
		}
		sent, err := e.SendMessage(ctx, agent, rbac.Actor(user.ID), in.Text, in.Key, in.Interrupt)
		id = sent.ID
		return err
	})
	if err != nil {
		return refuse(err, err.Error())
	}
	detail := "via " + in.Via
	if in.Answer != "" {
		detail += ", answers " + in.Answer
	} else {
		detail += ", " + id
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: action, Project: project, Agent: agent, Detail: detail, Allowed: true})
	return id, nil
}

// BridgeEvent is one entry of what bridges read: a message between an agent and a user, or a change a bridge acts on.
type BridgeEvent struct {
	Seq  int64     `json:"seq"` // its place in the outbox: a bridge resumes after the last one it handled
	Time time.Time `json:"time"`
	// Type is "message", or a change: "agent.removed", "project.removed", "account.linked", "account.unlinked", "user.disabled",
	// "role.changed".
	Type    string `json:"type"`
	Project string `json:"project,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// A message's.
	ID      string   `json:"id,omitempty"`
	From    string   `json:"from,omitempty"` // agent:<name> or user:<id>
	To      string   `json:"to,omitempty"`
	Kind    string   `json:"kind,omitempty"` // request, question, resolution, update
	Re      string   `json:"re,omitempty"`
	Text    string   `json:"text,omitempty"`
	Choices []string `json:"choices,omitempty"`
	Outcome string   `json:"outcome,omitempty"`
	// A change's: the user it is about, and the account linked or unlinked.
	User    string `json:"user,omitempty"`
	Bridge  string `json:"bridge,omitempty"`
	Account string `json:"account,omitempty"`
}

// Changes a bridge hears of.
const (
	EventMessage         = "message"
	EventAgentRemoved    = "agent.removed"
	EventProjectRemoved  = "project.removed"
	EventAccountLinked   = "account.linked"
	EventAccountUnlinked = "account.unlinked"
	EventUserDisabled    = "user.disabled"
	EventRoleChanged     = "role.changed"
)

// outboxRetention is how long the outbox keeps a message: a bridge away for longer misses it.
const outboxRetention = 7 * 24 * time.Hour

// cursorEvery is how often the cursor moves for events with nothing to keep: reading them again after a restart costs nothing, a write for
// each of them would.
const cursorEvery = 10 * time.Second

// handleEvent is one event of a project's events feed. What an agent and a user said to each other goes in the outbox for the bridges, and
// the cursor moves past the event in the same step. An error is an event that was not kept: the feed must not go past it.
func (h *Hub) handleEvent(ctx context.Context, project string, event links.Event) error {
	var body []byte
	keep := forUsers(event)
	if keep {
		var err error
		if keep, err = h.mayReceive(ctx, project, event); err != nil {
			return err
		}
	}
	if keep {
		body, _ = json.Marshal(BridgeEvent{Type: EventMessage, Time: event.Time, Project: project, Agent: event.Agent, ID: event.ID, From: event.Actor, To: event.To,
			Kind: event.Kind, Re: event.Re, Text: event.Text, Choices: event.Choices, Outcome: event.Outcome})
	}
	if body == nil && event.Type != "start" && !h.cursorDue(project) {
		return nil
	}
	seq, err := h.store.RecordEvent(ctx, project, event.Seq, event.Agent, body, event.Time)
	if err != nil {
		return err
	}
	h.cursorMoved(project)
	if seq != 0 {
		h.outboxChanged()
	}
	return nil
}

// mayReceive reports whether the user a message is addressed to may see it: an agent addresses anybody, and only a person who holds a role
// on the project hears from it. What a user said came through Deliver, which checked them. An error is not knowing: the event is not
// kept, and read again.
func (h *Hub) mayReceive(ctx context.Context, project string, event links.Event) (bool, error) {
	id, ok := strings.CutPrefix(event.To, "user:")
	if !ok {
		return true, nil
	}
	user, err := h.store.User(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || user.Disabled() {
		return false, err
	}
	_, err = h.access(ctx, user, project)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (h *Hub) cursorDue(project string) bool {
	h.cursorMu.Lock()
	defer h.cursorMu.Unlock()
	at, ok := h.cursorAt[project]
	return !ok || time.Since(at) >= cursorEvery
}

func (h *Hub) cursorMoved(project string) {
	h.cursorMu.Lock()
	defer h.cursorMu.Unlock()
	h.cursorAt[project] = time.Now()
}

// forUsers reports whether an event is a message one end of which is a hub user.
func forUsers(event links.Event) bool {
	return event.Type == "message" && event.Agent != "" && (strings.HasPrefix(event.To, "user:") || strings.HasPrefix(event.Actor, "user:"))
}

func (h *Hub) outboxChanged() {
	h.outboxMu.Lock()
	defer h.outboxMu.Unlock()
	close(h.outboxWake)
	h.outboxWake = make(chan struct{})
}

// Outbox lists what bridges read, after seq, at most limit messages, and a channel that closes when there is more. A negative seq is the end
// of the outbox: only what comes next. A seq past the end is a hub whose database was made anew: all of it is new to the bridge.
func (h *Hub) Outbox(ctx context.Context, after int64, limit int) ([]BridgeEvent, int64, <-chan struct{}, error) {
	h.outboxMu.Lock()
	wake := h.outboxWake
	h.outboxMu.Unlock()
	last, err := h.store.OutboxLast(ctx)
	if err != nil {
		return nil, after, wake, err
	}
	switch {
	case after < 0:
		after = last
	case after > last:
		after = 0
	}
	entries, err := h.store.OutboxAfter(ctx, after, limit)
	if err != nil {
		return nil, after, wake, err
	}
	out := make([]BridgeEvent, 0, len(entries))
	for _, e := range entries {
		var m BridgeEvent
		if json.Unmarshal(e.Body, &m) == nil {
			m.Seq = e.Seq
			out = append(out, m)
		}
		after = e.Seq
	}
	return out, after, wake, nil
}

// change puts a change in the outbox for the bridges.
func (h *Hub) change(ctx context.Context, e BridgeEvent) {
	e.Time = time.Now().UTC()
	body, _ := json.Marshal(e)
	// Kept apart from the project's row: a project that is forgotten takes its messages with it, never what says it was forgotten.
	if _, err := h.store.AddOutbox(context.WithoutCancel(ctx), "", e.Agent, body, e.Time); err != nil {
		h.log.Error("a change was not told to the bridges", "type", e.Type, "err", err)
		return
	}
	h.outboxChanged()
}

// eventsAfter is where a project's events feed resumes: -1 when the hub never read it. An error ends the feed, which the link opens again.
func (h *Hub) eventsAfter(project string) (int, error) {
	seq, err := h.store.EventCursor(context.Background(), project)
	if err != nil {
		h.log.Warn("read how far the events were read", "project", project, "err", err)
	}
	return seq, err
}
