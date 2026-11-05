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
	"sort"
	"sync"

	"github.com/egzo-ai/egzo/internal/access"
)

// errNoAccess is the answer for an agent whose access core never gave: one spawned by an egzo that did not.
var errNoAccess = &apiError{http.StatusNotFound, "what this agent may reach is not known yet: run `egzo project up`"}

// egzoSender is the address of the messages egzo itself writes, such as "your access changed". Nobody can send to it.
const egzoSender = "egzo"

// accessStore keeps what each agent may reach, as core described it at `up` and spawn (internal/access).
type accessStore struct{ mu sync.Mutex }

func (s *server) accessFile(agent string) string {
	return filepath.Join(s.dir, "access", agent+".json")
}

// readAccess reads what an agent may reach, as core last gave it.
func (s *server) readAccess(agent string) (access.Access, bool) {
	return readAccessFile(s.accessFile(agent))
}

// toldFile holds the access an agent had before the notice from egzo it has not resolved yet: a later change replaces
// that notice and says what changed since then, so nothing the agent was not told is lost.
func (s *server) toldFile(agent string) string {
	return filepath.Join(s.dir, "access", agent+".told.json")
}

func readAccessFile(path string) (access.Access, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return access.Access{}, false
	}
	var a access.Access
	if json.Unmarshal(data, &a) != nil {
		return access.Access{}, false
	}
	return a, true
}

func writeAccessFile(path string, a access.Access) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func (s *server) forgetAccess(agent string) error {
	for _, path := range []string{s.accessFile(agent), s.toldFile(agent)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// openNotices lists the notices from egzo an agent has not resolved.
func (s *server) openNotices(agent string) []Message {
	to := "agent:" + agent
	return s.events.messagesOf(to, func(m Message) bool {
		return m.To == to && m.From == egzoSender && m.Kind == kindRequest && m.State != stateResolved
	})
}

// supersede closes the notices from egzo that a newer one replaces, or that say nothing true any more, so an agent has
// at most one open and they never fill its open requests.
func (s *server) supersede(notices []Message, text string) error {
	outcome, _ := json.Marshal(map[string]string{"outcome": outcomeSuperseded})
	for _, m := range notices {
		if _, err := s.events.append(Event{Type: "resolved", Agent: involved(m.From, m.To), Actor: egzoSender, ID: m.ID, Text: text, Data: outcome}); err != nil {
			return err
		}
	}
	return nil
}

// outcomeSuperseded is the outcome of a notice from egzo that a newer one replaced. Only egzo closes a message this way.
const outcomeSuperseded = "superseded"

// putAccess stores what each named agent may reach. An agent whose access was known and changed gets a request from egzo
// that says what changed; a new agent, or one whose access is the same, gets nothing. A notice the agent has not resolved
// is replaced by the new one, which says what changed since the access the agent had before it. Unknown agents are
// skipped: an instance being removed may still be listed by the `up` that runs alongside.
func (s *server) putAccess(w http.ResponseWriter, r *http.Request) {
	var body map[string]access.Access
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplates)).Decode(&body); err != nil {
		http.Error(w, "expected a JSON object of agent names to their access", http.StatusBadRequest)
		return
	}
	s.access.mu.Lock()
	defer s.access.mu.Unlock()
	for _, agent := range sortedNames(body) {
		after := body[agent]
		if _, ok := s.agentFile(agent); !ok || !s.knownAgent(agent) {
			continue
		}
		before, known := s.readAccess(agent)
		if known && access.Equal(before, after) {
			continue
		}
		if err := s.storeAccess(agent, before, known, after); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// storeAccess keeps an agent's new access and tells the agent, replacing the notice it has not resolved.
func (s *server) storeAccess(agent string, before access.Access, known bool, after access.Access) error {
	if !known {
		return writeAccessFile(s.accessFile(agent), after)
	}
	open := s.openNotices(agent)
	if told, ok := readAccessFile(s.toldFile(agent)); ok && len(open) > 0 {
		before = told // the open notice is replaced: say what changed since the access the agent last knew
	} else if err := writeAccessFile(s.toldFile(agent), before); err != nil {
		return err
	}
	if err := writeAccessFile(s.accessFile(agent), after); err != nil {
		return err
	}
	if access.Equal(before, after) {
		return s.supersede(open, "your access is back to what it was")
	}
	text := fmt.Sprintf("Your network access changed: %s. Call the `access` tool (or run `egzo agent access`) for all of it, "+
		"and go on with what it was stopping. Resolve this once you have read it.", access.Changes(before, after))
	// closed first, so the agent's open requests have room for the new one
	if err := s.supersede(open, "replaced by a newer notice"); err != nil {
		return err
	}
	// a full inbox is not an error: the access is stored, the agent sees it at its next `access`
	_, _, _ = s.send(sendRequest{From: egzoSender, To: "agent:" + agent, Text: text})
	return nil
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
