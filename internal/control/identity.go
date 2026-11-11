// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/egzo-ai/egzo/internal/names"
)

// Identity is what other agents may know of an agent besides its name and activity: the template it is an instance of
// and the harness it runs. Core gives it at spawn and at every `up` (`PUT /identities`). Nothing else of the template
// (workspaces, egress, prompt, env) is shown to anyone: what agents need to know of each other is for the project
// owner's prompts.
type Identity struct {
	Template string `json:"template"`
	Harness  string `json:"harness"`
	// Starter is who started the agent (operator, user:<id>, service:<name>), and StarterName a hub user's name, as spawn gave it: the
	// agent is told, and nobody else. `up` gives no name, and the one spawn gave is kept.
	Starter     string `json:"starter,omitempty"`
	StarterName string `json:"starter_name,omitempty"`
}

func (s *server) identityFile(agent string) string {
	return filepath.Join(s.dir, "identity", agent+".json")
}

func (s *server) readIdentity(agent string) (Identity, bool) {
	data, err := os.ReadFile(s.identityFile(agent))
	if err != nil {
		return Identity{}, false
	}
	var id Identity
	if json.Unmarshal(data, &id) != nil {
		return Identity{}, false
	}
	return id, true
}

func (s *server) forgetIdentity(agent string) error {
	if err := os.Remove(s.identityFile(agent)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// putIdentities stores the template and harness of each named agent. Unknown agents are skipped, as for access: an
// instance being removed may still be listed by the `up` that runs alongside.
func (s *server) putIdentities(w http.ResponseWriter, r *http.Request) {
	var body map[string]Identity
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplates)).Decode(&body); err != nil {
		http.Error(w, "expected a JSON object of agent names to their template and harness", http.StatusBadRequest)
		return
	}
	for _, agent := range sortedNames(body) {
		if _, ok := s.agentFile(agent); !ok || !s.knownAgent(agent) {
			continue
		}
		id := body[agent]
		if before, ok := s.readIdentity(agent); ok && id.StarterName == "" && id.Starter == before.Starter {
			id.StarterName = before.StarterName
		}
		if id.Starter != "" && !names.Actor(id.Starter, "user", "service") || !validPersonName(id.StarterName) {
			http.Error(w, "a starter is operator, user:<id> or service:<name>, and its name one short line", http.StatusBadRequest)
			return
		}
		data, err := json.Marshal(id)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(s.identityFile(agent)), 0o755)
		}
		if err == nil {
			err = writeAtomic(s.identityFile(agent), data)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
