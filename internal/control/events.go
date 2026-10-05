package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one entry of the typed event stream. Every client, whether the CLI, the hub or a chat
// integration, reads the same stream, and every entry says who acted.
type Event struct {
	Seq   int             `json:"seq"`
	Time  time.Time       `json:"time"`
	Type  string          `json:"type"` // status, message, announced, fetched, resolved, unconfirmed, interrupt, interrupted, activity, hook
	Agent string          `json:"agent,omitempty"`
	Actor string          `json:"actor,omitempty"` // agent:<name> or user:<id>; operator for the CLI
	ID    string          `json:"id,omitempty"`    // message id
	Text  string          `json:"text,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// store is an append-only log on the control volume, with the live view derived from it: the messages
// and their states are kept in memory, rebuilt from the log when the sidecar starts.
type store struct {
	mu     sync.Mutex
	path   string
	events []Event
	wake   chan struct{} // closed and replaced whenever an event is appended

	messages map[string]*Message
	order    []string
}

func openStore(dir string) (*store, error) {
	s := &store{path: filepath.Join(dir, "events.jsonl"), wake: make(chan struct{}), messages: map[string]*Message{}}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			s.events = append(s.events, event)
			s.index(event)
		}
	}
	return s, scanner.Err()
}

func (s *store) append(event Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Seq = len(s.events) + 1
	event.Time = time.Now().UTC()
	line, err := json.Marshal(event)
	if err != nil {
		return event, err
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return event, err
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return event, err
	}
	s.events = append(s.events, event)
	s.index(event)
	close(s.wake)
	s.wake = make(chan struct{})
	return event, nil
}

// since returns the events after seq, filtered by agent when one is given, and a channel that
// closes when a newer event arrives.
func (s *store) since(seq int, agent string) ([]Event, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, event := range s.events {
		if event.Seq > seq && (agent == "" || event.Agent == agent) {
			out = append(out, event)
		}
	}
	return out, s.wake
}

// all returns the whole log without copying it: the log only ever grows, so what was returned
// stays valid.
func (s *store) all() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[:len(s.events):len(s.events)]
}

// activity is what an agent is doing: starting, idle, working or blocked. It is the last activity event.
func (s *store) activity(agent string) string {
	events := s.all()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "activity" && events[i].Agent == agent {
			return events[i].Text
		}
	}
	return ""
}

// interruptPending reports whether an interrupt was requested for an agent and not yet handed over.
func (s *store) interruptPending(agent string) bool {
	events := s.all()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Agent != agent {
			continue
		}
		switch events[i].Type {
		case "interrupted":
			return false
		case "interrupt":
			return true
		}
	}
	return false
}

// AgentStatus is what the control sidecar knows of an agent: the status line it set, its activity, and what
// it owes (open requests, an unanswered question).
type AgentStatus struct {
	Agent    string    `json:"agent"`
	Status   string    `json:"status"`
	Activity string    `json:"activity"`
	Open     int       `json:"open"`
	Waiting  bool      `json:"waiting"`
	Updated  time.Time `json:"updated"`
}

func (s *store) statuses() map[string]AgentStatus {
	latest := map[string]AgentStatus{}
	for _, event := range s.all() {
		switch event.Type {
		case "status":
			entry := latest[event.Agent]
			entry.Agent, entry.Status, entry.Updated = event.Agent, event.Text, event.Time
			latest[event.Agent] = entry
		case "activity":
			entry := latest[event.Agent]
			entry.Agent, entry.Activity = event.Agent, event.Text
			if entry.Updated.Before(event.Time) {
				entry.Updated = event.Time
			}
			latest[event.Agent] = entry
		}
	}
	return latest
}
