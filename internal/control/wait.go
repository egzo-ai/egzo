// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"
)

// MaxWait is the longest one wait call blocks. A harness aborts an MCP tool call that runs past its own timeout and shows
// the model an error: OpenCode's is 30 s, Codex's 60 s, Claude Code's hours. The agent port's write timeout is 30 s too.
// So a wait returns well before the smallest, and an agent that wants to wait longer calls it again.
const MaxWait = 25 * time.Second

// Waited is what wait answers: the messages it was asked about as their sender sees them, the reply to each one that is
// resolved, and what arrived for the caller in the meantime, so that waiting never hides a request.
type Waited struct {
	// Changed is false when the time ran out with nothing new.
	Changed bool      `json:"changed"`
	Sent    []Watched `json:"sent"`
	// Arrived lists the ids of messages for the caller that it was not told about when the wait began.
	Arrived []string `json:"arrived"`
}

// Watched is one message a wait follows.
type Watched struct {
	Sent
	// Reply is the id of the resolution, once the message is resolved: fetch it with get_message.
	Reply string `json:"reply,omitempty"`
}

var errWaitRunning = &apiError{http.StatusConflict, "a wait is already running for this agent: one at a time"}

// waiters keeps the agents with a wait running, so each has at most one.
type waiters struct {
	mu      sync.Mutex
	running map[string]bool
}

func (w *waiters) enter(agent string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running == nil {
		w.running = map[string]bool{}
	}
	if w.running[agent] {
		return false
	}
	w.running[agent] = true
	return true
}

func (w *waiters) leave(agent string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.running, agent)
}

// watched is the state of the messages a wait follows, as their sender sees them.
func (s *server) watched(agent string, ids []string) ([]Watched, error) {
	from := "agent:" + agent
	out := make([]Watched, 0, len(ids))
	for _, id := range ids {
		m, ok := s.events.message(id)
		if !ok || m.From != from || (m.Kind != kindRequest && m.Kind != kindQuestion) {
			return nil, errNoSuchMessage
		}
		w := Watched{Sent: Sent{Message: m}}
		for _, related := range s.events.messagesOf(from, func(r Message) bool { return r.To == from && r.Re == id }) {
			switch related.Kind {
			case kindUpdate:
				w.LatestUpdate = &Note{Text: related.Text, Time: related.Time}
			case kindResolution:
				// the reply is written just before the request is marked resolved: it is resolved from the reply on
				w.Reply, w.State, w.Outcome = related.ID, stateResolved, related.Outcome
			}
		}
		out = append(out, w)
	}
	return out, nil
}

func sameWatch(a, b []Watched) bool {
	return slices.EqualFunc(a, b, func(x, y Watched) bool {
		return x.State == y.State && x.Reply == y.Reply && (x.LatestUpdate == nil) == (y.LatestUpdate == nil) &&
			(x.LatestUpdate == nil || x.LatestUpdate.Time.Equal(y.LatestUpdate.Time))
	})
}

func (s *server) pendingFor(agent string) []string {
	var ids []string
	for _, m := range s.listFor(agent) {
		ids = append(ids, m.ID)
	}
	to := "agent:" + agent
	for _, m := range s.events.messagesOf(to, func(m Message) bool { return m.To == to && m.State == stateQueued && m.Kind != kindUpdate }) {
		ids = append(ids, m.ID)
	}
	return ids
}

// wait blocks until one of the messages the agent sent changes (fetched, updated, resolved), a message arrives for the
// agent, or the time runs out (at most MaxWait), and answers where they stand.
func (s *server) wait(ctx context.Context, agent string, ids []string, d time.Duration) (Waited, error) {
	if len(ids) == 0 || len(ids) > maxOpenPerRecipient {
		return Waited{}, badRequest("wait for 1 to %d messages you sent", maxOpenPerRecipient)
	}
	if d <= 0 || d > MaxWait {
		d = MaxWait
	}
	if !s.waiting.enter(agent) {
		return Waited{}, errWaitRunning
	}
	defer s.waiting.leave(agent)
	wake := s.events.waker()
	before, err := s.watched(agent, ids)
	if err != nil {
		return Waited{}, err
	}
	known := s.pendingFor(agent)
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		select {
		case <-wake:
		case <-deadline.C:
			return Waited{Sent: before, Arrived: []string{}}, nil
		case <-ctx.Done():
			return Waited{}, ctx.Err()
		}
		wake = s.events.waker()
		now, err := s.watched(agent, ids)
		if err != nil {
			return Waited{}, err
		}
		arrived := []string{}
		replies := map[string]bool{}
		for _, w := range now {
			replies[w.Reply] = true
		}
		for _, id := range s.pendingFor(agent) {
			if !slices.Contains(known, id) && !slices.Contains(arrived, id) && !replies[id] {
				arrived = append(arrived, id)
			}
		}
		if !sameWatch(before, now) || len(arrived) > 0 {
			return Waited{Changed: true, Sent: now, Arrived: arrived}, nil
		}
	}
}
