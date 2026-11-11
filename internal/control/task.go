// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// A task is what an agent was started to do, when it is not unattended: the first prompt typed into its terminal, as if the person who
// started it had typed it (they could, by attaching). It is the one free text control ever has typed: kept apart from the event log, so it
// is in no feed, and typed once, at the agent's first idle. An unattended agent gets its task as a request instead (stack.Spawn).

func (s *server) taskFile(agent string) string { return filepath.Join(s.dir, "tasks", agent) }

// putTask keeps the task of an agent spawn just registered.
func (s *server) putTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.agentFile(name); !ok || !s.knownAgent(name) {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "expected {\"text\": the task}", http.StatusBadRequest)
		return
	}
	if !validText(body.Text) {
		http.Error(w, textTooLong, http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.taskFile(name)), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeAtomic(s.taskFile(name), []byte(body.Text)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// takeTask gives an agent's task once: it is gone when the holder types it.
func (s *server) takeTask(agent string) (string, bool) {
	data, err := os.ReadFile(s.taskFile(agent))
	if err != nil {
		return "", false
	}
	if err := os.Remove(s.taskFile(agent)); err != nil {
		logger.Error("forgetting a task once typed", "agent", agent, "err", err)
		return "", false // never typed twice
	}
	return string(data), true
}

func (s *server) forgetTask(agent string) error {
	if err := os.Remove(s.taskFile(agent)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
