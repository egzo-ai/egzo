package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Message kinds.
const (
	kindRequest    = "request"    // something one party asks another to do
	kindQuestion   = "question"   // an agent asks the sender of a request something
	kindResolution = "resolution" // what resolve sends back: a result, or the answer to a question
	kindUpdate     = "update"     // progress on a request
)

// Message states. A request or question goes queued, announced, fetched, resolved. A resolution is
// closed once fetched; an update is never announced.
const (
	stateQueued      = "queued"
	stateAnnounced   = "announced"
	stateFetched     = "fetched"
	stateResolved    = "resolved"
	stateUnconfirmed = "unconfirmed"
)

// Limits: a loop between agents is easy to start.
const (
	maxOpenPerRecipient = 20
	maxThreadDepth      = 8
	maxSendsPerMinute   = 30
	maxAnnouncements    = 3
)

var outcomes = map[string]bool{"done": true, "declined": true, "failed": true}

// Message is one message and where it stands, derived from the log.
type Message struct {
	ID       string    `json:"id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Kind     string    `json:"kind"`
	Text     string    `json:"text"`
	Re       string    `json:"re,omitempty"`
	State    string    `json:"state"`
	Outcome  string    `json:"outcome,omitempty"`
	Choices  []string  `json:"choices,omitempty"`
	Time     time.Time `json:"time"`
	Hops     int       `json:"-"`
	Attempts int       `json:"-"`
	Deadline time.Time `json:"-"`
}

// messageData is what a `message` event carries besides its text.
type messageData struct {
	To      string   `json:"to"`
	Kind    string   `json:"kind"`
	Re      string   `json:"re,omitempty"`
	Hops    int      `json:"hops"`
	Outcome string   `json:"outcome,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// index folds one event into the message view. It runs under the store's lock.
func (s *store) index(event Event) {
	switch event.Type {
	case "message":
		var data messageData
		if json.Unmarshal(event.Data, &data) != nil || event.ID == "" {
			return
		}
		s.messages[event.ID] = &Message{
			ID: event.ID, From: event.Actor, To: data.To, Kind: data.Kind, Text: event.Text, Re: data.Re,
			State: stateQueued, Outcome: data.Outcome, Choices: data.Choices, Time: event.Time, Hops: data.Hops,
		}
		s.order = append(s.order, event.ID)
	case "announced":
		if m := s.messages[event.ID]; m != nil && m.State != stateResolved {
			var data struct {
				Attempt    int   `json:"attempt"`
				DeadlineMs int64 `json:"deadline_ms"`
			}
			json.Unmarshal(event.Data, &data)
			m.State, m.Attempts = stateAnnounced, data.Attempt
			m.Deadline = time.UnixMilli(data.DeadlineMs)
		}
	case "fetched":
		if m := s.messages[event.ID]; m != nil && m.State != stateResolved {
			m.State = stateFetched
		}
	case "unconfirmed":
		if m := s.messages[event.ID]; m != nil && m.State == stateAnnounced {
			m.State = stateUnconfirmed
		}
	case "resolved":
		if m := s.messages[event.ID]; m != nil {
			m.State = stateResolved
			var data struct {
				Outcome string `json:"outcome"`
			}
			json.Unmarshal(event.Data, &data)
			m.Outcome = data.Outcome
		}
	}
}

// message returns a copy of one message.
func (s *store) message(id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.messages[id]
	if m == nil {
		return Message{}, false
	}
	return *m, true
}

// selectMessages returns copies of the messages that match, oldest first.
func (s *store) selectMessages(match func(Message) bool) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Message{}
	for _, id := range s.order {
		if m := *s.messages[id]; match(m) {
			out = append(out, m)
		}
	}
	return out
}

// An apiError carries the HTTP status a failure maps to; MCP shows its text.
type apiError struct {
	Status int
	Text   string
}

func (e *apiError) Error() string { return e.Text }

var errNoSuchMessage = &apiError{http.StatusNotFound, "no such message"}

func badRequest(format string, a ...any) *apiError {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func conflict(format string, a ...any) *apiError {
	return &apiError{http.StatusConflict, fmt.Sprintf(format, a...)}
}

var userAddress = regexp.MustCompile(`^user:[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// parseAddress accepts operator, user:<id> and agent:<name>.
func parseAddress(address string) (kind, name string, ok bool) {
	switch {
	case address == "operator":
		return "operator", "", true
	case userAddress.MatchString(address):
		return "user", strings.TrimPrefix(address, "user:"), true
	case strings.HasPrefix(address, "agent:") && safeName.MatchString(strings.TrimPrefix(address, "agent:")):
		return "agent", strings.TrimPrefix(address, "agent:"), true
	}
	return "", "", false
}

func isHuman(address string) bool {
	kind, _, ok := parseAddress(address)
	return ok && kind != "agent"
}

func agentOf(address string) string {
	if kind, name, ok := parseAddress(address); ok && kind == "agent" {
		return name
	}
	return ""
}

func newMessageID() string {
	raw := make([]byte, 16)
	rand.Read(raw)
	return "m" + hex.EncodeToString(raw)
}

// involved is the agent an event about a message is filed under, so `events --agent` shows the
// conversation of one agent: the recipient when it is an agent, otherwise the agent sending.
func involved(from, to string) string {
	if name := agentOf(to); name != "" {
		return name
	}
	return agentOf(from)
}

// rateLimit lets a sender send maxSendsPerMinute messages a minute. Agents only: a person at the CLI
// is not a loop.
func (s *server) rateLimit(from string, now time.Time) error {
	if agentOf(from) == "" {
		return nil
	}
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.sends == nil {
		s.sends = map[string][]time.Time{}
	}
	recent := s.sends[from][:0]
	for _, t := range s.sends[from] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxSendsPerMinute {
		s.sends[from] = recent
		return &apiError{http.StatusTooManyRequests, fmt.Sprintf("at most %d messages a minute: wait a little", maxSendsPerMinute)}
	}
	s.sends[from] = append(recent, now)
	return nil
}

// sendRequest is what the verbs that create messages take.
type sendRequest struct {
	From, To, Text, Re, Kind string
	Choices                  []string
	Interrupt                bool
}

// send creates a message after checking who it is for, its thread and the limits. It is the one way a
// request or a question comes to exist.
func (s *server) send(req sendRequest) (Message, Event, error) {
	if !validText(req.Text) {
		return Message{}, Event{}, badRequest("text must not be empty or longer than 16 KB")
	}
	kind, name, ok := parseAddress(req.To)
	if !ok {
		return Message{}, Event{}, badRequest("%q is not an address: use operator, user:<id> or agent:<name>", req.To)
	}
	if kind == "agent" {
		if !s.knownAgent(name) {
			return Message{}, Event{}, &apiError{http.StatusNotFound, fmt.Sprintf("no agent %q in this project", name)}
		}
		if req.To == req.From {
			return Message{}, Event{}, badRequest("an agent cannot send a message to itself")
		}
	}
	hops := 0
	if req.Re != "" {
		parent, found := s.events.message(req.Re)
		if !found || (parent.To != req.From && parent.From != req.From) {
			return Message{}, Event{}, errNoSuchMessage
		}
		hops = parent.Hops + 1
		if hops >= maxThreadDepth {
			return Message{}, Event{}, badRequest("this thread is already %d messages deep: finish it or start a new one", maxThreadDepth)
		}
	}
	if kind == "agent" {
		open := s.events.selectMessages(func(m Message) bool {
			return m.To == req.To && (m.Kind == kindRequest || m.Kind == kindQuestion) && m.State != stateResolved
		})
		if len(open) >= maxOpenPerRecipient {
			return Message{}, Event{}, conflict("%s already has %d open requests: wait for it to resolve some", req.To, maxOpenPerRecipient)
		}
	}
	if err := s.rateLimit(req.From, time.Now()); err != nil {
		return Message{}, Event{}, err
	}
	return s.write(req, hops)
}

// write appends a message event, and an interrupt when one was asked for.
func (s *server) write(req sendRequest, hops int) (Message, Event, error) {
	kind := req.Kind
	if kind == "" {
		kind = kindRequest
	}
	data, _ := json.Marshal(messageData{To: req.To, Kind: kind, Re: req.Re, Hops: hops, Choices: req.Choices})
	id := newMessageID()
	event, err := s.events.append(Event{Type: "message", Agent: involved(req.From, req.To), Actor: req.From, ID: id, Text: req.Text, Data: data})
	if err != nil {
		return Message{}, Event{}, err
	}
	if req.Interrupt && agentOf(req.To) != "" {
		if _, err := s.events.append(Event{Type: "interrupt", Agent: agentOf(req.To), Actor: req.From}); err != nil {
			return Message{}, Event{}, err
		}
	}
	message, _ := s.events.message(id)
	return message, event, nil
}

// ownMessage finds a message addressed to an agent. A message that does not exist and one that
// belongs to someone else look the same.
func (s *server) ownMessage(agent, id string) (Message, error) {
	m, ok := s.events.message(id)
	if !ok || m.To != "agent:"+agent {
		return Message{}, errNoSuchMessage
	}
	return m, nil
}

// fetch gives an agent a message addressed to it and marks it fetched: fetching is the
// acknowledgement of its announcement.
func (s *server) fetch(agent, id string) (Message, error) {
	m, err := s.ownMessage(agent, id)
	if err != nil {
		return Message{}, err
	}
	if m.State == stateQueued || m.State == stateAnnounced || m.State == stateUnconfirmed {
		if _, err := s.events.append(Event{Type: "fetched", Agent: agent, Actor: "agent:" + agent, ID: id}); err != nil {
			return Message{}, err
		}
		m, _ = s.events.message(id)
	}
	return m, nil
}

// openRequest finds a request or question an agent has fetched and not resolved, which is what
// resolve, update and ask work on.
func (s *server) openRequest(agent, id string) (Message, error) {
	m, err := s.ownMessage(agent, id)
	if err != nil {
		return Message{}, err
	}
	switch {
	case m.Kind != kindRequest && m.Kind != kindQuestion:
		return Message{}, badRequest("only a request or a question can be answered; this is a %s", m.Kind)
	case m.State == stateResolved:
		return Message{}, conflict("message %s is already resolved", id)
	case m.State != stateFetched:
		return Message{}, conflict("fetch message %s first", id)
	}
	return m, nil
}

// resolveMessage closes a request or question and sends the result back to whoever sent it.
func (s *server) resolveMessage(actor string, m Message, text, outcome string) (string, error) {
	if !outcomes[outcome] {
		return "", badRequest("outcome must be done, declined or failed")
	}
	if !validText(text) {
		return "", badRequest("text must not be empty or longer than 16 KB")
	}
	data, _ := json.Marshal(messageData{To: m.From, Kind: kindResolution, Re: m.ID, Hops: m.Hops, Outcome: outcome})
	id := newMessageID()
	if _, err := s.events.append(Event{Type: "message", Agent: involved(actor, m.From), Actor: actor, ID: id, Text: text, Data: data}); err != nil {
		return "", err
	}
	resolved, _ := json.Marshal(map[string]string{"outcome": outcome, "resolution": id})
	_, err := s.events.append(Event{Type: "resolved", Agent: involved(m.From, m.To), Actor: actor, ID: m.ID, Text: text, Data: resolved})
	return id, err
}

// resolve is an agent closing a request it fetched.
func (s *server) resolve(agent, id, text, outcome string) (string, error) {
	m, err := s.openRequest(agent, id)
	if err != nil {
		return "", err
	}
	return s.resolveMessage("agent:"+agent, m, text, outcome)
}

// resolveForPerson is a person closing what was addressed to them: answering a question, or closing a
// request an agent made of them. No fetch comes first: a person reads their messages in the CLI.
func (s *server) resolveForPerson(actor, id, text, outcome string) (string, error) {
	m, ok := s.events.message(id)
	if !ok {
		return "", errNoSuchMessage
	}
	if !isHuman(m.To) {
		return "", badRequest("message %s is addressed to %s: only it can resolve it", id, m.To)
	}
	if m.Kind != kindRequest && m.Kind != kindQuestion {
		return "", badRequest("only a request or a question can be answered; this is a %s", m.Kind)
	}
	if m.State == stateResolved {
		return "", conflict("message %s is already resolved", id)
	}
	return s.resolveMessage(actor, m, text, outcome)
}

// update tells the sender of a request how it is going. It is passive: never announced.
func (s *server) update(agent, id, text string) (string, error) {
	m, err := s.openRequest(agent, id)
	if err != nil {
		return "", err
	}
	if !validText(text) {
		return "", badRequest("text must not be empty or longer than 16 KB")
	}
	message, _, err := s.write(sendRequest{From: "agent:" + agent, To: m.From, Text: text, Re: id, Kind: kindUpdate}, m.Hops)
	return message.ID, err
}

// ask puts a question to the sender of a request, and leaves the request open.
func (s *server) ask(agent, id, text string, choices []string) (string, error) {
	m, err := s.openRequest(agent, id)
	if err != nil {
		return "", err
	}
	message, _, err := s.send(sendRequest{From: "agent:" + agent, To: m.From, Text: text, Re: id, Kind: kindQuestion, Choices: choices})
	return message.ID, err
}

// sendFromAgent is the message tool: a new request from an agent.
func (s *server) sendFromAgent(agent, to, text, re string) (string, error) {
	message, _, err := s.send(sendRequest{From: "agent:" + agent, To: to, Text: text, Re: re})
	return message.ID, err
}

// listFor is list_messages: what an agent owes and has not read, never what has not been announced.
func (s *server) listFor(agent string) []Message {
	to := "agent:" + agent
	return s.events.selectMessages(func(m Message) bool {
		if m.To != to {
			return false
		}
		switch m.Kind {
		case kindRequest, kindQuestion:
			return m.State == stateAnnounced || m.State == stateFetched || m.State == stateUnconfirmed
		case kindResolution:
			return m.State == stateAnnounced || m.State == stateUnconfirmed
		case kindUpdate:
			return m.State == stateQueued
		}
		return false
	})
}

// overlay is what an agent owes: the requests it fetched and has not resolved, and whether a question
// it asked is waiting for an answer.
func (s *server) overlay(agent string) (open int, waiting bool) {
	to, from := "agent:"+agent, "agent:"+agent
	for _, m := range s.events.selectMessages(func(m Message) bool { return m.To == to || m.From == from }) {
		if m.To == to && (m.Kind == kindRequest || m.Kind == kindQuestion) && m.State == stateFetched {
			open++
		}
		if m.From == from && m.Kind == kindQuestion && m.State != stateResolved {
			waiting = true
		}
	}
	return open, waiting
}
