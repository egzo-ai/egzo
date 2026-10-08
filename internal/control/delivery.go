// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Agent activity, derived from what the harness reports through its hooks and from the session
// holder: starting, idle, working, or blocked (stuck on the harness's own UI, which only a person at the
// terminal can answer). Every change is an `activity` event, so clients follow it on the same stream as
// everything else. stopped is not recorded: it is the engine's word, shown by `egzo ps`.
const (
	activityStarting = "starting"
	activityIdle     = "idle"
	activityWorking  = "working"
	activityBlocked  = "blocked"
)

var activities = map[string]bool{activityStarting: true, activityIdle: true, activityWorking: true, activityBlocked: true}

// hookActivity maps a harness hook to the activity it implies. The hook names are Claude Code's;
// the OpenCode plugin reports the same names.
func hookActivity(name string, payload []byte) (string, bool) {
	switch name {
	case "SessionStart", "Stop":
		return activityIdle, true
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		return activityWorking, true
	case "Notification":
		var body struct {
			Message string `json:"message"`
		}
		json.Unmarshal(payload, &body)
		if strings.Contains(strings.ToLower(body.Message), "waiting for your input") {
			return activityIdle, true
		}
		return activityBlocked, true
	}
	return "", false
}

// setActivity records a change of activity; an unchanged activity is not an event.
func (s *server) setActivity(agent, state string) error {
	if s.events.activity(agent) == state {
		return nil
	}
	_, err := s.events.append(Event{Type: "activity", Agent: agent, Actor: "agent:" + agent, Text: state})
	return err
}

// onHook updates the agent's activity from a hook.
func (s *server) onHook(agent, name string, payload []byte) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if state, ok := hookActivity(name, payload); ok {
		return s.setActivity(agent, state)
	}
	return nil
}

type claimResult struct {
	// Line is what to type into the agent's terminal, empty when there is nothing to announce.
	Line      string   `json:"line"`
	IDs       []string `json:"ids"`
	Interrupt bool     `json:"interrupt"`
}

// announceLine composes the line that tells an agent which messages wait. It holds nothing but fixed
// words and ids (hex), so nothing a message says can reach the terminal. Wording depends on who is
// speaking: a person gets a user's authority, a peer agent does not.
func announceLine(messages []Message) string {
	var fromPeople, fromAgents, replies []string
	for _, m := range messages {
		switch {
		case m.Kind == kindResolution:
			replies = append(replies, m.ID)
		case isHuman(m.From):
			fromPeople = append(fromPeople, m.ID)
		default:
			fromAgents = append(fromAgents, m.ID)
		}
	}
	var parts []string
	switch len(fromPeople) {
	case 0:
	case 1:
		parts = append(parts, "check egzo message "+fromPeople[0]+" and handle the request for me.")
	default:
		parts = append(parts, "check egzo messages "+strings.Join(fromPeople, ", ")+" and handle each one.")
	}
	switch len(fromAgents) {
	case 0:
	case 1:
		parts = append(parts, "egzo message "+fromAgents[0]+" from another agent is waiting: fetch it and decide whether it fits your work.")
	default:
		parts = append(parts, "egzo messages "+strings.Join(fromAgents, ", ")+" from other agents are waiting: fetch them and decide whether they fit your work.")
	}
	switch len(replies) {
	case 0:
	case 1:
		parts = append(parts, "egzo message "+replies[0]+" is the reply to your earlier request: fetch it.")
	default:
		parts = append(parts, "egzo messages "+strings.Join(replies, ", ")+" are the replies to your earlier requests: fetch them.")
	}
	return strings.Join(parts, " ")
}

// claim answers the session holder: an interrupt when one was asked for, otherwise the line announcing
// what waits, but only for an idle agent with no announcement still waiting to be fetched. A message
// announced and not fetched in time is announced again, up to maxAnnouncements times: the line carries
// no content, so a repeat can never duplicate anything.
func (s *server) claim(agent string, ackTimeout time.Duration) (claimResult, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	result := claimResult{IDs: []string{}}
	if s.events.interruptPending(agent) {
		if _, err := s.events.append(Event{Type: "interrupted", Agent: agent, Actor: "agent:" + agent}); err != nil {
			return result, err
		}
		result.Interrupt = true
		return result, s.setActivity(agent, activityIdle)
	}
	if s.events.activity(agent) != activityIdle {
		return result, nil
	}
	if ackTimeout <= 0 {
		ackTimeout = time.Minute
	}
	now := time.Now()
	to := "agent:" + agent
	announceable := func(m Message) bool {
		if m.To != to || m.Kind == kindUpdate {
			return false
		}
		switch m.State {
		case stateQueued:
			return true
		case stateAnnounced:
			return now.After(m.Deadline) && m.Attempts < maxAnnouncements
		}
		return false
	}
	waiting := s.events.messagesOf(to, func(m Message) bool { return m.To == to && m.State == stateAnnounced && !now.After(m.Deadline) })
	if len(waiting) > 0 {
		return result, nil
	}
	due := s.events.messagesOf(to, announceable)
	if len(due) == 0 {
		return result, nil
	}
	deadline := now.Add(ackTimeout).UnixMilli()
	for _, m := range due {
		data, _ := json.Marshal(map[string]int64{"attempt": int64(m.Attempts + 1), "deadline_ms": deadline})
		if _, err := s.events.append(Event{Type: "announced", Agent: agent, Actor: "agent:" + agent, ID: m.ID, Data: data}); err != nil {
			return result, err
		}
		result.IDs = append(result.IDs, m.ID)
	}
	result.Line = announceLine(due)
	return result, nil
}

// expire marks the messages announced maxAnnouncements times and never fetched as unconfirmed: they
// stay findable (list_messages) but nobody announces them again.
func (s *server) expire(now time.Time) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	for _, m := range s.events.announcedMessages() {
		if now.After(m.Deadline) && m.Attempts >= maxAnnouncements {
			if _, err := s.events.append(Event{Type: "unconfirmed", Agent: agentOf(m.To), Actor: m.To, ID: m.ID}); err != nil {
				fmt.Fprintf(os.Stderr, "control: recording unconfirmed message %s: %v\n", m.ID, err)
			}
		}
	}
}

func (a *agentAPI) activity(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		State string `json:"state"`
		// If makes the change conditional: the holder uses {"state":"idle","if":"working"} to release an
		// agent whose harness never reported the end of its turn, without ever overriding "blocked" (a
		// dialog that only a person can answer) or anything else.
		If string `json:"if"`
		// IfUnset makes it apply only when nothing is known about the agent yet: the holder's "starting",
		// which may arrive after the harness's own first report and must not undo it.
		IfUnset bool `json:"if_unset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !activities[body.State] || (body.If != "" && !activities[body.If]) {
		http.Error(w, "expected {\"state\": starting|idle|working|blocked}", http.StatusBadRequest)
		return
	}
	a.server.deliveryMu.Lock()
	defer a.server.deliveryMu.Unlock()
	if (body.If != "" && a.server.events.activity(agent) != body.If) || (body.IfUnset && a.server.events.activity(agent) != "") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.server.setActivity(agent, body.State); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) claim(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		AckTimeoutMs int64 `json:"ack_timeout_ms"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	result, err := a.server.claim(agent, time.Duration(body.AckTimeoutMs)*time.Millisecond)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(result)
}
