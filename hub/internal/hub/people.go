// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

// What the hub tells each project of the users who started its agents (the link's `people` verb): their names, and whether a chat bridge
// can reach them, never which platform nor why not. The project tells an agent when the person who started it becomes reachable, or
// unreachable. The hub sends a project the whole list whenever it changes: when the project's agents change, and when one of those users
// links or unlinks a chat account, is disabled or enabled, or has their role changed.

// peopleLimit is how long the hub takes over telling one project.
const peopleLimit = 30 * time.Second

type peopleState struct {
	mu    sync.Mutex
	sent  map[string]map[string]links.Person // by project: what it was told last
	locks map[string]*sync.Mutex             // by project: one telling at a time, so the last one sent is the latest
}

// forget makes the next update of a project tell it again: a new connection may be a project that lost what it was told.
func (p *peopleState) forget(project string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sent, project)
}

func (p *peopleState) lock(project string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks == nil {
		p.locks, p.sent = map[string]*sync.Mutex{}, map[string]map[string]links.Person{}
	}
	if p.locks[project] == nil {
		p.locks[project] = &sync.Mutex{}
	}
	return p.locks[project]
}

// peopleOf is what a project should know of the users who started its agents; false when the hub does not know the project's agents.
func (h *Hub) peopleOf(ctx context.Context, project string) (map[string]links.Person, bool) {
	p, ok := h.knownProject(project)
	if !ok {
		return nil, false
	}
	out := map[string]links.Person{}
	for _, a := range p.Agents {
		if id, isUser := strings.CutPrefix(a.Actor, "user:"); isUser {
			if _, done := out[a.Actor]; !done {
				out[a.Actor] = h.person(ctx, project, id)
			}
		}
	}
	return out, true
}

// person is one user as a project is told: reachable when a bridge would deliver to them what their agents send, which is the rule of
// Deliver the other way: they are enabled, may chat with the agents they started, and have a chat account linked.
func (h *Hub) person(ctx context.Context, project, id string) links.Person {
	user, err := h.store.User(ctx, id)
	if err != nil {
		return links.Person{}
	}
	p := links.Person{Name: displayName(user)}
	if user.Disabled() {
		return p
	}
	role, err := h.access(ctx, user, project)
	if err != nil || !rbac.Can(user.Subject(), role, rbac.Chat, rbac.Actor(user.ID)) {
		return p
	}
	if _, err := h.store.LatestBridgeAccount(ctx, id); err != nil {
		return p
	}
	p.Reachable = true
	return p
}

// tellPeople sends a project what it should know of its starters, when that is not what it was told last.
func (h *Hub) tellPeople(project string) {
	lock := h.people.lock(project)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), peopleLimit)
	defer cancel()
	now, ok := h.peopleOf(ctx, project)
	if !ok {
		return
	}
	h.people.mu.Lock()
	last, told := h.people.sent[project]
	h.people.mu.Unlock()
	if told && maps.Equal(last, now) || !told && len(now) == 0 {
		return
	}
	if err := h.call(ctx, project, func(ctx context.Context, e links.Engine) error { return e.People(ctx, now) }); err != nil {
		h.log.Warn("tell a project who started its agents", "project", project, "err", err)
		return
	}
	h.people.mu.Lock()
	h.people.sent[project] = now
	h.people.mu.Unlock()
}

// peopleChanged tells every project the hub holds what changed of its starters, in the background: a user's accounts, roles or state
// changed, and any project may have agents they started.
func (h *Hub) peopleChanged() {
	h.mu.RLock()
	projects := make([]string, 0, len(h.held))
	for id := range h.held {
		projects = append(projects, id)
	}
	h.mu.RUnlock()
	for _, project := range projects {
		go h.tellPeople(project)
	}
}
