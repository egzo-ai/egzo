// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// reportUndelivered is the hub saying that a message an agent sent one of its users did not reach them: {"reason": ...}, in words the hub
// chose for the agent.
func (s *server) reportUndelivered(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		http.Error(w, "expected {\"reason\": why it was not delivered}", http.StatusBadRequest)
		return
	}
	if err := s.undelivered(r.PathValue("id"), body.Reason); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// undelivered tells an agent, by a notice from egzo, that a message it sent a person did not reach them. The retry is the agent's: the notice
// says how. A question or a request nobody will see is closed as failed, so the agent does not wait for an answer that cannot come; a result
// stays as the agent closed the request. An update is a progress line and gets no notice: what ends the same request tells. Told twice, it
// is one notice.
func (s *server) undelivered(id, reason string) error {
	m, ok := s.events.message(id)
	if !ok {
		return errNoSuchMessage
	}
	agent := agentOf(m.From)
	if agent == "" || !strings.HasPrefix(m.To, "user:") {
		return badRequest("message %s is not from an agent to one of the hub's users", id)
	}
	reason = strings.TrimRight(strings.TrimSpace(reason), ".")
	if reason == "" || !validText(reason) {
		return badRequest("say why it was not delivered, in at most %d KB", maxTextSize>>10)
	}
	closing := false
	switch m.Kind {
	case kindUpdate:
		return nil
	case kindRequest, kindQuestion:
		if m.State == stateResolved { // answered, or closed when told before
			return nil
		}
		closing = true
	}
	text := fmt.Sprintf("Your %s %s to %s was not delivered: %s. It began: %q.", kindWord(m.Kind), m.ID, m.To, reason, beginning(m.Text))
	switch {
	case closing:
		text += " egzo closed it as failed: no answer will come."
	case m.Kind == kindResolution:
		text += " The request it answered stays resolved."
	}
	text += fmt.Sprintf(" If it still matters, send it again with message(to %q, re %q), now or later. Resolve this once you have read it.", m.To, m.ID)
	// the notice first: told again after a failure in between, the notice is not sent twice, and what is still open is closed
	if _, _, err := s.send(sendRequest{From: egzoSender, To: "agent:" + agent, Text: text, Key: "undelivered:" + m.ID, Notice: noticeUndelivered}); err != nil {
		return err
	}
	if closing {
		outcome, _ := json.Marshal(map[string]string{"outcome": "failed"})
		if _, err := s.events.append(Event{Type: "resolved", Agent: agent, Actor: egzoSender, ID: m.ID, Text: "not delivered: " + reason, Data: outcome}); err != nil {
			return err
		}
	}
	return nil
}

func kindWord(kind string) string {
	if kind == kindResolution {
		return "result"
	}
	return kind
}

// beginning is the start of a text, enough for an agent to know which of its messages it is.
func beginning(text string) string {
	const most = 120
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= most {
		return text
	}
	return string([]rune(text)[:most]) + "…"
}
