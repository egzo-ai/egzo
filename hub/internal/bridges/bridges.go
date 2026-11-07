// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package bridges is the API the chat bridges use (docs/hub-bridges.md), under /bridge/v1. The hub is the broker: a bridge reads what agents
// and users said to each other from the outbox, as a stream of JSON lines that resumes after the last one it handled, and posts what users
// say, which the hub checks before it goes anywhere. A bridge signs in with its own token, which names it.
package bridges

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	corenames "github.com/egzo-ai/egzo/internal/names"
)

const (
	// Prefix is where the API lives.
	Prefix = "/bridge/v1"
	// maxIn is the longest body a bridge posts: a message's text is at most 16 KB.
	maxIn = 64 << 10
	// batch is how many messages are read from the outbox at a time.
	batch = 100
	// deliverLimit is how long the hub takes over one inbound message.
	deliverLimit = 30 * time.Second
)

// keepAlive is how often a quiet stream says it is alive, with an empty line: reverse proxies cut a silent connection.
var keepAlive = 15 * time.Second

var projectID = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// Inbound is what a bridge posts: a user's message to an agent, or their answer to what the agent asked.
type Inbound struct {
	Project   string `json:"project"`
	Agent     string `json:"agent"`
	User      string `json:"user"` // the hub user id the bridge linked the person to
	Text      string `json:"text"`
	Key       string `json:"key,omitempty"` // the platform's id for the message: the same key is the same message
	Interrupt bool   `json:"interrupt,omitempty"`
	Answer    string `json:"answer,omitempty"` // the id of the question, or request, this answers
	Outcome   string `json:"outcome,omitempty"`
}

// Server serves the bridges.
type Server struct {
	hub    *hub.Hub
	tokens map[[32]byte]string // sha256 of the secret -> the bridge's name
	log    *slog.Logger
}

// New serves the bridges named in bridges (name -> secret, from config.ParseBridges).
func New(h *hub.Hub, bridges map[string]string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{hub: h, tokens: map[[32]byte]string{}, log: log}
	for name, secret := range bridges {
		s.tokens[sha256.Sum256([]byte(secret))] = name
	}
	return s
}

// Handler is the API, to mount at Prefix.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/messages", s.authed(s.stream))
	mux.HandleFunc("POST "+Prefix+"/messages", s.authed(s.deliver))
	return mux
}

// authed finds the bridge a token names, comparing with every token in constant time.
func (s *Server) authed(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))
		name := ""
		for known, bridge := range s.tokens {
			if subtle.ConstantTimeCompare(sum[:], known[:]) == 1 {
				name = bridge
			}
		}
		if !ok || name == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="egzo bridges"`)
			answer(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r, name)
	}
}

// stream writes the outbox after ?after= as JSON lines, and stays open for what comes next. after=-1, or none, is only what comes next.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, bridge string) {
	after := int64(-1)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			answer(w, http.StatusBadRequest, map[string]string{"error": "after is a number"})
			return
		}
		after = n
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		answer(w, http.StatusInternalServerError, map[string]string{"error": "cannot stream"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) // a stream lasts as long as the bridge reads it
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	s.log.Info("a bridge reads the outbox", "bridge", bridge, "after", after)
	encoder := json.NewEncoder(w)
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		messages, at, wake, err := s.hub.Outbox(r.Context(), after, batch)
		if err != nil {
			if r.Context().Err() == nil {
				s.log.Warn("read the outbox", "bridge", bridge, "err", err)
			}
			return
		}
		after = at
		for _, m := range messages {
			if err := encoder.Encode(m); err != nil {
				return
			}
		}
		flusher.Flush()
		if len(messages) == batch {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-tick.C:
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// deliver takes one message from a bridge. It answers its id, or that it was not delivered and never why: the hub's audit log says why.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, bridge string) {
	var in Inbound
	if err := json.NewDecoder(io.LimitReader(r.Body, maxIn)).Decode(&in); err != nil || !wellFormed(in) {
		s.log.Info("a bridge posted what is not a message", "bridge", bridge)
		answer(w, http.StatusBadRequest, map[string]string{"error": "not a message"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), deliverLimit)
	defer cancel()
	id, err := s.hub.Deliver(ctx, in.Project, in.Agent, hub.Inbound{Via: bridge, User: in.User, Text: in.Text, Key: in.Key,
		Interrupt: in.Interrupt, Answer: in.Answer, Outcome: in.Outcome})
	if err != nil {
		status := http.StatusForbidden
		var offline hub.OfflineError
		if errors.As(err, &offline) {
			status = http.StatusServiceUnavailable // the bridge may say the project is away; nothing queues
		}
		answer(w, status, map[string]string{"error": "not delivered"})
		return
	}
	answer(w, http.StatusOK, map[string]string{"id": id})
}

// wellFormed checks the shape of what a bridge posts; the hub checks the person.
func wellFormed(in Inbound) bool {
	return projectID.MatchString(in.Project) && corenames.Instance(in.Agent) && in.User != "" && len(in.User) <= 64 &&
		strings.TrimSpace(in.Text) != "" && (in.Answer != "" || in.Key != "")
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
