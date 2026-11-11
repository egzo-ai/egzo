// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Why a message to a person did not reach them (docs/hub-bridges.md). The codes say what happened, never on which platform: every bridge
// uses the same. The hub sets the first two; a bridge reports the others.
const (
	UndeliveredNoAccount   = "no-account"         // the person linked no chat account
	UndeliveredNoAccess    = "no-access"          // the person may not hear from the project
	UndeliveredUnreachable = "person-unreachable" // the platform will not let the bridge reach the person
	UndeliveredNoPlace     = "no-place"           // the bridge has nowhere to put it
	UndeliveredRefused     = "refused"            // the platform refused it for good
	UndeliveredUnavailable = "unavailable"        // the platform did not take it in the bridge's retry window
)

// undeliveredLimit is how long telling an agent may take.
const undeliveredLimit = 30 * time.Second

// Undelivered is a bridge saying that a message the hub gave it did not reach the person. A bridge reports only what it was given, for the
// account it was given. The agent is told why, in the hub's words; detail, the platform's own, goes to the audit log only. An update is a
// progress line, and the agent is not told of it.
func (h *Hub) Undelivered(ctx context.Context, bridge, id, account, code, detail string) error {
	o, err := h.store.OutboxMessage(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var e BridgeEvent
	if json.Unmarshal(o.Body, &e) != nil || e.Type != EventMessage || e.Bridge != bridge || e.Account != account {
		return ErrNotFound
	}
	h.record(ctx, store.AuditEntry{Actor: bridge + ":" + account, Action: "agent.undelivered", Project: o.Project, Agent: e.Agent,
		Detail: fmt.Sprintf("via %s, %s: %s %s", bridge, id, code, clipDetail(detail)), Allowed: true})
	if !fromAgent(e.From) || e.Kind == "update" {
		return nil
	}
	reason := h.undeliveredReason(ctx, code, e.To, bridge)
	return h.call(ctx, o.Project, func(ctx context.Context, eng links.Engine) error { return eng.Undelivered(ctx, id, reason) })
}

// notDelivered tells an agent, in the background, that the hub kept a message of its to a person from every bridge. The feed goes on: the
// event is read again after a restart, and the agent told again, which makes no second notice.
func (h *Hub) notDelivered(project string, event links.Event, code string) {
	if !fromAgent(event.Actor) || event.Kind == "update" {
		return
	}
	h.record(context.Background(), store.AuditEntry{Actor: "hub", Action: "agent.undelivered", Project: project, Agent: event.Agent,
		Detail: event.ID + ": " + code, Allowed: true})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), undeliveredLimit)
		defer cancel()
		reason := h.undeliveredReason(ctx, code, event.To, "")
		if err := h.call(ctx, project, func(ctx context.Context, eng links.Engine) error { return eng.Undelivered(ctx, event.ID, reason) }); err != nil {
			h.log.Warn("tell an agent that its message was not delivered", "project", project, "agent", event.Agent, "message", event.ID, "err", err)
		}
	}()
}

func fromAgent(actor string) bool { return strings.HasPrefix(actor, "agent:") }

// undeliveredReason is what the agent reads: the person by their hub name, and the platform by the bridge's. An unknown code is a refusal.
func (h *Hub) undeliveredReason(ctx context.Context, code, to, bridge string) string {
	person := to
	if u, err := h.store.User(ctx, strings.TrimPrefix(to, "user:")); err == nil {
		person = u.Username
	}
	platform := platformName(bridge)
	switch code {
	case UndeliveredNoAccount:
		return person + " has no chat account linked to the hub"
	case UndeliveredNoAccess:
		return person + " may not hear from this project"
	case UndeliveredUnreachable:
		return person + " cannot be reached on " + platform
	case UndeliveredNoPlace:
		return "there is nowhere to reach " + person + " on " + platform
	case UndeliveredUnavailable:
		return platform + " did not take it in time"
	default:
		return platform + " refused it"
	}
}

// platformName is a bridge's name as people write the platform: discord is Discord.
func platformName(bridge string) string {
	r, size := utf8.DecodeRuneInString(bridge)
	if r == utf8.RuneError {
		return "the chat platform"
	}
	return string(unicode.ToUpper(r)) + bridge[size:]
}

// clipDetail keeps what a bridge says of a failure short, for the audit log.
func clipDetail(detail string) string {
	const most = 300
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > most {
		return detail[:most] + "…"
	}
	return detail
}
