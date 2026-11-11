// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// Person is what the hub says of one of its users who started agents of this project (the `people` verb): their name, and whether a chat
// bridge can reach them now. Never which platform, nor why not.
type Person struct {
	Name      string `json:"name"`
	Reachable bool   `json:"reachable"`
}

// noticeStarter is the notice egzo sends an agent when the person who started it can, or can no longer, be reached.
const noticeStarter = "starter"

// maxPersonName is the longest name of a person, as the hub gives it.
const maxPersonName = 100

// people is what the hub last said of the project's starters.
type people struct {
	mu sync.Mutex
}

func (s *server) peopleFile() string { return filepath.Join(s.dir, "people.json") }

// readPeople is what the hub last said, by user:<id>; nothing when it never said.
func (s *server) readPeople() map[string]Person {
	out := map[string]Person{}
	data, err := os.ReadFile(s.peopleFile())
	if err != nil || json.Unmarshal(data, &out) != nil {
		return map[string]Person{}
	}
	return out
}

// putPeople takes what the hub says of the project's starters now, all of them: it replaces what it said before. An agent whose starter
// was known and became reachable, or unreachable, is told; a person said of for the first time, or the same again, is no news.
func (s *server) putPeople(w http.ResponseWriter, r *http.Request) {
	var body map[string]Person
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplates)).Decode(&body); err != nil {
		http.Error(w, "expected a JSON object of user:<id> to {name, reachable}", http.StatusBadRequest)
		return
	}
	for id, p := range body {
		if !strings.HasPrefix(id, "user:") || !names.Actor(id, "user") {
			http.Error(w, fmt.Sprintf("%q is not a hub user: user:<id>", id), http.StatusBadRequest)
			return
		}
		if !validPersonName(p.Name) {
			http.Error(w, fmt.Sprintf("the name of %s is not one line of at most %d characters", id, maxPersonName), http.StatusBadRequest)
			return
		}
	}
	s.people.mu.Lock()
	defer s.people.mu.Unlock()
	before := s.readPeople()
	data, _ := json.Marshal(body)
	if err := os.MkdirAll(s.dir, 0o755); err == nil {
		err = writeAtomic(s.peopleFile(), data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, id := range sortedNames(body) {
		if was, known := before[id]; known && was.Reachable != body[id].Reachable {
			if err := s.tellStarted(id, body[id]); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func validPersonName(name string) bool {
	return len(name) <= maxPersonName && termsafe.Line(name) == name && !strings.ContainsAny(name, "\n\r")
}

// tellStarted sends each agent a person started the notice that they can, or can no longer, be reached, replacing the one it has not read.
func (s *server) tellStarted(id string, p Person) error {
	who := id
	if p.Name != "" {
		who = p.Name + " (" + id + ")"
	}
	text := fmt.Sprintf("%s can be reached again: send them what you were waiting to ask. Resolve this once you have read it.", who)
	if !p.Reachable {
		text = fmt.Sprintf("%s, who started you, can't be reached any more: what you send them is not delivered. If you cannot go on "+
			"without them, set your status to what you are waiting for, so the people who watch the project see it. You will be told "+
			"when they can be reached again. Resolve this once you have read it.", who)
	}
	for _, agent := range s.projectAgents() {
		if identity, ok := s.readIdentity(agent); !ok || identity.Starter != id {
			continue
		}
		to := "agent:" + agent
		open := s.events.messagesOf(to, func(m Message) bool {
			return m.To == to && m.From == egzoSender && m.Notice == noticeStarter && m.Kind == kindRequest && m.State != stateResolved
		})
		if err := s.supersede(open, "replaced by a newer notice"); err != nil {
			return err
		}
		// a full inbox is not an error: the agent sees it at its next `agents`
		if _, _, err := s.send(sendRequest{From: egzoSender, To: to, Text: text, Notice: noticeStarter}); err != nil {
			var failure *apiError
			if !errors.As(err, &failure) {
				return err
			}
		}
	}
	return nil
}

// StartedBy is who started an agent, as it sees it in `agents`: their id, their name, and whether they can be reached, when the hub said.
type StartedBy struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Reachable *bool  `json:"reachable,omitempty"`
}

// startedBy is who started an agent; nil when it is not known (an agent registered before egzo said).
func (s *server) startedBy(agent string) *StartedBy {
	identity, ok := s.readIdentity(agent)
	if !ok || identity.Starter == "" {
		return nil
	}
	out := &StartedBy{ID: identity.Starter, Name: identity.StarterName}
	if p, ok := s.readPeople()[identity.Starter]; ok {
		if p.Name != "" {
			out.Name = p.Name
		}
		reachable := p.Reachable
		out.Reachable = &reachable
	}
	return out
}
