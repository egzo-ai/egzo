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

func (s *server) readAccess(agent string) (access.Access, bool) {
	data, err := os.ReadFile(s.accessFile(agent))
	if err != nil {
		return access.Access{}, false
	}
	var a access.Access
	if json.Unmarshal(data, &a) != nil {
		return access.Access{}, false
	}
	return a, true
}

func (s *server) forgetAccess(agent string) error {
	if err := os.Remove(s.accessFile(agent)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// putAccess stores what each named agent may reach. An agent whose access was known and changed gets a request from egzo
// that says what changed; a new agent, or one whose access is the same, gets nothing. Unknown agents are skipped: an
// instance being removed may still be listed by the `up` that runs alongside.
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
		data, err := json.Marshal(after)
		if err == nil {
			if err = os.MkdirAll(filepath.Dir(s.accessFile(agent)), 0o755); err == nil {
				err = writeAtomic(s.accessFile(agent), data)
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !known {
			continue
		}
		text := fmt.Sprintf("Your network access changed: %s. Call the `access` tool (or run `egzo agent access`) for all of it, "+
			"and go on with what it was stopping. Resolve this once you have read it.", access.Changes(before, after))
		if _, _, err := s.send(sendRequest{From: egzoSender, To: "agent:" + agent, Text: text}); err != nil {
			// a full inbox or a rate limit: the access is stored, the agent sees it at its next `access`
			continue
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
