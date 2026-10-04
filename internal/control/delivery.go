package control

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Agent activity, derived from what the harness reports through its hooks and from the session
// holder: starting, idle, busy, or blocked (waiting on a human). Every change is an `activity`
// event, so clients follow it on the same stream as everything else.
const (
	activityStarting = "starting"
	activityIdle     = "idle"
	activityBusy     = "busy"
	activityBlocked  = "blocked"
)

var activities = map[string]bool{activityStarting: true, activityIdle: true, activityBusy: true, activityBlocked: true}

// hookActivity maps a harness hook to the activity it implies. The hook names are Claude Code's;
// the OpenCode plugin reports the same names.
func hookActivity(name string, payload []byte, current string) (string, bool) {
	switch name {
	case "SessionStart", "Stop":
		return activityIdle, true
	case "UserPromptSubmit":
		return activityBusy, true
	case "PreToolUse", "PostToolUse":
		return activityBusy, true
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

var messageHeader = regexp.MustCompile(`\[egzo msg (m[0-9a-f]+)[ \]]`)

// acknowledge marks the outstanding messages of an agent whose ids appear in text as delivered.
func (s *server) acknowledge(agent string, ids map[string]bool) error {
	for _, message := range s.events.outstanding(agent) {
		if ids[message.ID] {
			if _, err := s.events.append(Event{Type: messageDelivered, Agent: agent, Actor: "agent:" + agent, ID: message.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// onHook updates the agent's activity from a hook, and acknowledges the messages a prompt carries.
func (s *server) onHook(agent, name string, payload []byte) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if name == "UserPromptSubmit" {
		var body struct {
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(payload, &body) == nil {
			ids := map[string]bool{}
			for _, found := range messageHeader.FindAllStringSubmatch(body.Prompt, -1) {
				ids[found[1]] = true
			}
			if len(ids) > 0 {
				if err := s.acknowledge(agent, ids); err != nil {
					return err
				}
			}
		}
	}
	if state, ok := hookActivity(name, payload, s.events.activity(agent)); ok {
		return s.setActivity(agent, state)
	}
	return nil
}

type claimedMessage struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Text string `json:"text"`
}

type claimResult struct {
	Messages  []claimedMessage `json:"messages"`
	Interrupt bool             `json:"interrupt"`
}

// claim hands the session holder what to type: an interrupt when one was asked for, otherwise all
// queued messages at once, but only when the agent is idle and nothing earlier is still waiting for
// its acknowledgement.
func (s *server) claim(agent string, ackTimeout time.Duration) (claimResult, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	result := claimResult{Messages: []claimedMessage{}}
	if s.events.interruptPending(agent) {
		if _, err := s.events.append(Event{Type: "interrupted", Agent: agent, Actor: "agent:" + agent}); err != nil {
			return result, err
		}
		result.Interrupt = true
		return result, s.setActivity(agent, activityIdle)
	}
	if s.events.activity(agent) != activityIdle || len(s.events.outstanding(agent)) > 0 {
		return result, nil
	}
	if ackTimeout <= 0 {
		ackTimeout = time.Minute
	}
	deadline, _ := json.Marshal(map[string]int64{"deadline_ms": time.Now().Add(ackTimeout).UnixMilli()})
	for _, message := range s.events.pending(agent) {
		if _, err := s.events.append(Event{Type: messageDelivering, Agent: agent, Actor: "agent:" + agent, ID: message.ID, Data: deadline}); err != nil {
			return result, err
		}
		result.Messages = append(result.Messages, claimedMessage{ID: message.ID, From: message.From, Text: message.Text})
	}
	return result, nil
}

// expire marks the messages nobody acknowledged in time as unconfirmed. They are never retried: a
// second paste could duplicate a message the harness did get.
func (s *server) expire(now time.Time) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	seen := map[string]bool{}
	for _, event := range s.events.all() {
		if event.Type != messageDelivering || seen[event.Agent] {
			continue
		}
		seen[event.Agent] = true
		for _, message := range s.events.outstanding(event.Agent) {
			if !message.Deadline.IsZero() && now.After(message.Deadline) {
				s.events.append(Event{Type: messageUnconfirmed, Agent: event.Agent, Actor: "agent:" + event.Agent, ID: message.ID})
			}
		}
	}
}

func (a *agentAPI) activity(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !activities[body.State] {
		http.Error(w, "expected {\"state\": starting|idle|busy|blocked}", http.StatusBadRequest)
		return
	}
	a.server.deliveryMu.Lock()
	defer a.server.deliveryMu.Unlock()
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

func (a *agentAPI) ack(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "expected {\"ids\": [...]}", http.StatusBadRequest)
		return
	}
	ids := map[string]bool{}
	for _, id := range body.IDs {
		ids[id] = true
	}
	a.server.deliveryMu.Lock()
	defer a.server.deliveryMu.Unlock()
	if err := a.server.acknowledge(agent, ids); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
