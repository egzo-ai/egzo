// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// A person links their chat account to their hub user once: the bridge asks for a code, the person opens the hub's address with it,
// signed in, and confirms. From then on the hub knows whose account a message comes from (Deliver), and bridges hear it was linked.

// ErrBadBridgeCode is a bridge code that is wrong, expired or used.
var ErrBadBridgeCode = store.ErrBadCode

// BridgeCode makes a code for a person's chat account, for the bridge to show them. It is the only time the code is in the clear.
func (h *Hub) BridgeCode(ctx context.Context, bridge, account, name string) (string, time.Time, error) {
	return h.store.CreateBridgeCode(ctx, bridge, account, name, time.Now())
}

// BridgeCodeFor says what a code is for, for the page that asks the person to confirm it.
func (h *Hub) BridgeCodeFor(ctx context.Context, code string) (store.BridgeCode, error) {
	return h.store.BridgeCodeFor(ctx, code, time.Now())
}

// ConfirmBridgeCode links the code's chat account to the user who confirms it, signed in.
func (h *Hub) ConfirmBridgeCode(ctx context.Context, user store.User, code string) (store.BridgeAccount, error) {
	a, err := h.store.ConfirmBridgeCode(ctx, code, user.ID, time.Now())
	if err != nil {
		h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "account.link", Detail: "refused: a bad or expired code"})
		return a, err
	}
	a.Username = user.Username
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "account.link", Detail: fmt.Sprintf("%s account %s (%s)", a.Bridge, a.ExternalName, a.ExternalID), Allowed: true})
	h.change(ctx, BridgeEvent{Type: EventAccountLinked, User: user.ID, Bridge: a.Bridge, Account: a.ExternalID})
	return a, nil
}

// BridgeAccountsOf lists the chat accounts a user linked.
func (h *Hub) BridgeAccountsOf(ctx context.Context, user store.User) ([]store.BridgeAccount, error) {
	return h.store.BridgeAccountsOf(ctx, user.ID)
}

// UnlinkBridgeAccount removes one of the user's chat accounts.
func (h *Hub) UnlinkBridgeAccount(ctx context.Context, user store.User, bridge, account string) error {
	ok, err := h.store.UnlinkBridgeAccount(ctx, user.ID, bridge, account)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "account.unlink", Detail: bridge + " account " + account, Allowed: true})
	h.change(ctx, BridgeEvent{Type: EventAccountUnlinked, User: user.ID, Bridge: bridge, Account: account})
	return nil
}

// BridgeAccounts lists the accounts linked through one bridge, for the bridge: who its people are on the hub.
func (h *Hub) BridgeAccounts(ctx context.Context, bridge string) ([]store.BridgeAccount, error) {
	return h.store.BridgeAccounts(ctx, bridge)
}

// accountUser is the enabled hub user a chat account is linked to.
func (h *Hub) accountUser(ctx context.Context, bridge, account string) (store.User, error) {
	a, err := h.store.BridgeAccountOf(ctx, bridge, account)
	if err != nil {
		return store.User{}, ErrForbidden
	}
	user, err := h.store.User(ctx, a.UserID)
	if err != nil || user.Disabled() {
		return store.User{}, ErrForbidden
	}
	return user, nil
}

// MayAdd says whether the person behind a chat account may add an agent to a place on their platform: an admin of the agent's project.
// The platform decides who sees the place; the hub checks every message anyway.
func (h *Hub) MayAdd(ctx context.Context, bridge, account, project, agent string) (bool, error) {
	user, err := h.accountUser(ctx, bridge, account)
	if err != nil {
		h.record(ctx, store.AuditEntry{Actor: bridge + ":" + account, Action: "agent.add-to-place", Project: project, Agent: agent, Detail: "refused, via " + bridge + ": a chat account nobody linked"})
		return false, nil
	}
	role, err := h.access(ctx, user, project)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := h.creatorOf(ctx, project, agent); err != nil {
		return false, nil
	}
	allowed := role.AtLeast(rbac.Admin)
	entry := store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "agent.add-to-place", Project: project, Agent: agent, Detail: "via " + bridge, Allowed: allowed}
	if !allowed {
		entry.Detail = fmt.Sprintf("refused, via %s: needs admin, holds %s", bridge, role)
	}
	h.record(ctx, entry)
	return allowed, nil
}

// ChatAgent is an agent a person may chat with, as a bridge lists it.
type ChatAgent struct {
	Project     string `json:"project"` // the project's id on the hub
	ProjectName string `json:"project_name"`
	Agent       string `json:"agent"`
	Template    string `json:"template"`
	State       string `json:"state"`
	Admin       bool   `json:"admin"` // the person may add it to a place
}

// ChatAgents lists the agents the person behind a chat account may chat with, for a bridge's lists and autocompletes.
func (h *Hub) ChatAgents(ctx context.Context, bridge, account string) ([]ChatAgent, error) {
	user, err := h.accountUser(ctx, bridge, account)
	if err != nil {
		return nil, err
	}
	refs, err := h.Projects(ctx, user)
	if err != nil {
		return nil, err
	}
	out := []ChatAgent{}
	for _, ref := range refs {
		for _, a := range ref.Project.Agents {
			if rbac.Can(user.Subject(), ref.Role, rbac.Chat, a.Actor) {
				out = append(out, ChatAgent{Project: ref.ID, ProjectName: ref.Project.Name, Agent: a.Name, Template: a.Template, State: a.State,
					Admin: ref.Role.AtLeast(rbac.Admin)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectName != out[j].ProjectName {
			return out[i].ProjectName < out[j].ProjectName
		}
		return out[i].Agent < out[j].Agent
	})
	return out, nil
}

// What the hub knows of an agent a bridge keeps a place for (AgentsPresent).
const (
	PresencePresent = "present" // in its project, as the project's link last said
	PresenceAbsent  = "absent"  // not in its project, or the project is gone from the hub
	PresenceUnknown = "unknown" // the project's link has not said since the hub started: ask again later
)

// AgentRef names an agent of a project.
type AgentRef struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
}

// AgentPresence is whether an agent is still in its project.
type AgentPresence struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
	State   string `json:"state"` // PresencePresent, PresenceAbsent or PresenceUnknown
}

// AgentsPresent says, for each agent, whether its project still has it. A bridge keeps an agent's places for a while after the agent
// leaves, since re-creating an agent removes it and starts it again under the same name, and asks this to know when to let them go. It
// answers from what the links last pushed, and never asks a link: absent is only what the hub knows, unknown is a project it has not
// heard from since it started. A revoked or forgotten project is absent: it never comes back under the same id.
func (h *Hub) AgentsPresent(ctx context.Context, refs []AgentRef) ([]AgentPresence, error) {
	projects := map[string]map[string]bool{} // project -> its agents; nil when unknown
	gone := map[string]bool{}
	out := make([]AgentPresence, 0, len(refs))
	for _, r := range refs {
		if _, seen := projects[r.Project]; !seen && !gone[r.Project] {
			reg, err := h.store.Registration(ctx, r.Project)
			switch {
			case errors.Is(err, store.ErrNotFound) || err == nil && reg.State != hublink.StateApproved:
				gone[r.Project] = true
			case err != nil:
				return nil, err
			default:
				var agents map[string]bool
				if p, known := h.knownProject(r.Project); known {
					agents = map[string]bool{}
					for _, a := range p.Agents {
						agents[a.Name] = true
					}
				}
				projects[r.Project] = agents
			}
		}
		state := PresenceAbsent
		switch agents := projects[r.Project]; {
		case gone[r.Project]:
		case agents == nil:
			state = PresenceUnknown
		case agents[r.Agent]:
			state = PresencePresent
		}
		out = append(out, AgentPresence{Project: r.Project, Agent: r.Agent, State: state})
	}
	return out, nil
}
