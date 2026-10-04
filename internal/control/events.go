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
	Type  string          `json:"type"` // status, say, question, answer, message, delivered, hook
	Agent string          `json:"agent,omitempty"`
	Actor string          `json:"actor,omitempty"` // agent:<name> or user:<id>; operator for the CLI
	ID    string          `json:"id,omitempty"`    // message or question id
	Text  string          `json:"text,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// store is an append-only log on the control volume, with the live view derived from it.
type store struct {
	mu     sync.Mutex
	path   string
	events []Event
	wake   chan struct{} // closed and replaced whenever an event is appended
}

func openStore(dir string) (*store, error) {
	s := &store{path: filepath.Join(dir, "events.jsonl"), wake: make(chan struct{})}
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

func (s *store) all() []Event {
	events, _ := s.since(0, "")
	return events
}

// Message is a queued message and its fate, derived from the log.
type Message struct {
	ID        string    `json:"id"`
	To        string    `json:"to"`
	From      string    `json:"from"`
	Text      string    `json:"text"`
	Time      time.Time `json:"time"`
	Delivered bool      `json:"delivered"`
}

// pending lists the undelivered messages for an agent, oldest first.
func (s *store) pending(agent string) []Message {
	delivered := map[string]bool{}
	var queued []Message
	for _, event := range s.all() {
		switch event.Type {
		case "message":
			if event.Agent == agent {
				queued = append(queued, Message{ID: event.ID, To: event.Agent, From: event.Actor, Text: event.Text, Time: event.Time})
			}
		case "delivered":
			delivered[event.ID] = true
		}
	}
	var out []Message
	for _, message := range queued {
		if !delivered[message.ID] {
			out = append(out, message)
		}
	}
	return out
}

// Question is a question an agent asked a human, with its answer once there is one.
type Question struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Text       string    `json:"text"`
	Time       time.Time `json:"time"`
	Answered   bool      `json:"answered"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"`
}

func (s *store) questions() []Question {
	byID := map[string]*Question{}
	var order []string
	for _, event := range s.all() {
		switch event.Type {
		case "question":
			byID[event.ID] = &Question{ID: event.ID, Agent: event.Agent, Text: event.Text, Time: event.Time}
			order = append(order, event.ID)
		case "answer":
			if question := byID[event.ID]; question != nil && !question.Answered {
				question.Answered, question.Answer, question.AnsweredBy = true, event.Text, event.Actor
			}
		}
	}
	out := make([]Question, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

// AgentStatus is the latest status an agent reported.
type AgentStatus struct {
	Agent   string    `json:"agent"`
	Status  string    `json:"status"`
	Updated time.Time `json:"updated"`
}

func (s *store) statuses() []AgentStatus {
	latest := map[string]AgentStatus{}
	for _, event := range s.all() {
		if event.Type == "status" {
			latest[event.Agent] = AgentStatus{Agent: event.Agent, Status: event.Text, Updated: event.Time}
		}
	}
	out := make([]AgentStatus, 0, len(latest))
	for _, status := range latest {
		out = append(out, status)
	}
	return out
}
