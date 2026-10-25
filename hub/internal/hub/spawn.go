// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// A spawn takes as long as the engine needs to create the agent's containers, which can be longer than a browser, or a proxy in
// front of the hub, waits for an answer. So a spawn is recorded and run in the background, apart from the request that asked for
// it, and the project lists it until it has ended: whoever can see the project sees it, and whoever asked learns how it ended.
// What is pending lives in memory: a hub that restarts forgets it (an agent that was made is in the project).

const (
	// spawnDeadline is how long one spawn may take, waiting for the ones before it in the same project included.
	spawnDeadline = 10 * time.Minute
	// spawnKept is how long an ended spawn stays in its project's view, for whoever asked to learn how it ended.
	spawnKept = 10 * time.Minute
)

// The states of a spawn.
const (
	SpawnPending = "spawning"
	SpawnReady   = "ready"
	SpawnFailed  = "failed"
)

// Spawn is one spawn the hub was asked for.
type Spawn struct {
	ID       string
	Template string
	Name     string // as asked: empty lets the engine choose
	Actor    string // who asked (rbac.Actor)
	State    string
	Agent    string // the agent's name, once it is ready
	Err      string // why it failed
	Started  time.Time
	Ended    time.Time
}

type spawning struct {
	mu     sync.Mutex
	byProj map[projectKey][]*Spawn
	last   map[projectKey]chan struct{} // the newest spawn of each project: the next one waits for it, so they run in order, one at a time
	done   map[string]chan struct{}     // by spawn id: closed when it ends
	wg     sync.WaitGroup
}

func (s *spawning) init() {
	if s.byProj == nil {
		s.byProj, s.last, s.done = map[projectKey][]*Spawn{}, map[projectKey]chan struct{}{}, map[string]chan struct{}{}
	}
}

// StartSpawn checks that the user may spawn from the project, records the spawn and starts it; it returns before the agent exists,
// which belongs to the user.
// The spawn goes on if the request that asked for it goes away.
func (h *Hub) StartSpawn(ctx context.Context, user store.User, engine, project, template, name string) (Spawn, error) {
	if _, err := h.authorize(ctx, user, engine, project, name, rbac.Spawn, "", "agent.spawn"); err != nil {
		return Spawn{}, err
	}
	key := projectKey{engine, project}
	s := &Spawn{ID: newSpawnID(), Template: template, Name: name, Actor: rbac.Actor(user.ID), State: SpawnPending, Started: time.Now()}
	h.spawns.mu.Lock()
	h.spawns.init()
	h.spawns.byProj[key] = append(h.spawns.byProj[key], s)
	done := make(chan struct{})
	h.spawns.done[s.ID] = done
	before := h.spawns.last[key] // the engine's project lock would make a spawn wait for the one before it anyway, and not in order
	h.spawns.last[key] = done
	started := *s
	h.spawns.mu.Unlock()

	h.spawns.wg.Add(1)
	go func() {
		defer h.spawns.wg.Done()
		defer close(done)
		run, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnDeadline)
		defer cancel()
		if before != nil {
			select {
			case <-before:
			case <-run.Done():
			}
		}
		var agent string
		err := run.Err()
		if err == nil {
			err = h.call(run, engine, func(e engines.Engine) (err error) {
				agent, err = e.Spawn(run, project, template, name, s.Actor)
				return err
			})
			h.afterOperation(run, engine)
		}
		h.spawns.mu.Lock()
		s.Ended = time.Now()
		if err != nil {
			s.State, s.Err = SpawnFailed, err.Error()
			h.log.Warn("spawn failed", "engine", engine, "project", project, "template", template, "actor", s.Actor, "err", err)
		} else {
			s.State, s.Agent = SpawnReady, agent
		}
		h.spawns.mu.Unlock()
	}()
	return started, nil
}

// Spawn spawns an agent and waits until it exists or the spawn has failed: StartSpawn, for a caller that has the time.
func (h *Hub) Spawn(ctx context.Context, user store.User, engine, project, template, name string) (string, error) {
	started, err := h.StartSpawn(ctx, user, engine, project, template, name)
	if err != nil {
		return "", err
	}
	h.spawns.mu.Lock()
	done := h.spawns.done[started.ID]
	h.spawns.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ended, _ := h.spawnByID(engine, project, started.ID)
	if ended.State == SpawnFailed {
		return "", spawnError(ended.Err)
	}
	return ended.Agent, nil
}

// WaitForSpawns waits until every spawn that was started has ended.
func (h *Hub) WaitForSpawns() { h.spawns.wg.Wait() }

// spawnsOf is what a project's view lists: the spawns still running and those that ended less than spawnKept ago, oldest first.
func (h *Hub) spawnsOf(engine, project string) []Spawn {
	key := projectKey{engine, project}
	h.spawns.mu.Lock()
	defer h.spawns.mu.Unlock()
	h.spawns.init()
	kept := h.spawns.byProj[key][:0]
	var list []Spawn
	for _, s := range h.spawns.byProj[key] {
		if s.State != SpawnPending && time.Since(s.Ended) > spawnKept {
			delete(h.spawns.done, s.ID)
			continue
		}
		kept = append(kept, s)
		list = append(list, *s)
	}
	if len(kept) == 0 { // nothing runs: the last one has ended
		delete(h.spawns.byProj, key)
		delete(h.spawns.last, key)
	} else {
		h.spawns.byProj[key] = kept
	}
	return list
}

func (h *Hub) spawnByID(engine, project, id string) (Spawn, bool) {
	h.spawns.mu.Lock()
	defer h.spawns.mu.Unlock()
	for _, s := range h.spawns.byProj[projectKey{engine, project}] {
		if s.ID == id {
			return *s, true
		}
	}
	return Spawn{}, false
}

type spawnError string

func (e spawnError) Error() string { return string(e) }

func newSpawnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sp" + hex.EncodeToString(b)
}
