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
	Project string `json:"project"`
	Agent   string `json:"agent"`
	Account string `json:"account"` // the person's account on the platform, which they linked to a hub user
	Text    string `json:"text"`
	// Key is the platform's id for the message, or for the answer (the click, the reply): the same key with the same message is a retry,
	// and with another one a mistake. It is required, at most maxKey printable characters.
	Key       string `json:"key,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
	Answer    string `json:"answer,omitempty"` // the id of the question, or request, this answers
	Outcome   string `json:"outcome,omitempty"`
}

// Server serves the bridges.
type Server struct {
	hub    *hub.Hub
	tokens map[[32]byte]string // sha256 of the secret -> the bridge's name
	public string              // the hub's address, for the link a person opens to confirm a code
	log    *slog.Logger
}

// LinkPath is the web page where a person confirms a bridge code. The code goes after its #: a browser sends it to no server, and no
// log holds it.
const LinkPath = "/chat-link"

// New serves the bridges named in bridges (name -> secret, from config.ParseBridges). public is the hub's address people use.
func New(h *hub.Hub, bridges map[string]string, public string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{hub: h, tokens: map[[32]byte]string{}, public: strings.TrimSuffix(public, "/"), log: log}
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
	mux.HandleFunc("POST "+Prefix+"/messages/{id}/undelivered", s.authed(s.undelivered))
	mux.HandleFunc("POST "+Prefix+"/codes", s.authed(s.code))
	mux.HandleFunc("GET "+Prefix+"/accounts", s.authed(s.accounts))
	mux.HandleFunc("GET "+Prefix+"/agents", s.authed(s.agents))
	mux.HandleFunc("POST "+Prefix+"/may-add", s.authed(s.mayAdd))
	mux.HandleFunc("POST "+Prefix+"/presence", s.authed(s.presence))
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
			if m.Type == hub.EventMessage && m.Bridge != "" && m.Bridge != bridge {
				continue // a message to a person goes to one bridge, the one of the account they linked last
			}
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
// Two answers say more, and tell nothing a person does not know: 409 "already answered", to the person who answered, and 504 "not
// confirmed", a project that did not answer in time, which may have taken it; sent again with the same key, it is the same message.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, bridge string) {
	var in Inbound
	if err := json.NewDecoder(io.LimitReader(r.Body, maxIn)).Decode(&in); err != nil || !wellFormed(in) {
		s.log.Info("a bridge posted what is not a message", "bridge", bridge)
		answer(w, http.StatusBadRequest, map[string]string{"error": "not a message"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), deliverLimit)
	defer cancel()
	id, err := s.hub.Deliver(ctx, in.Project, in.Agent, hub.Inbound{Via: bridge, Account: in.Account, Text: in.Text, Key: in.Key,
		Interrupt: in.Interrupt, Answer: in.Answer, Outcome: in.Outcome})
	if errors.Is(err, hub.ErrAnsweredAlready) { // only ever the person who answered: it tells them nothing new
		answer(w, http.StatusConflict, map[string]string{"error": "already answered"})
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil && r.Context().Err() == nil {
		// The project did not answer in time: it may have taken the message. The bridge may send it again with the same key.
		answer(w, http.StatusGatewayTimeout, map[string]string{"error": "not confirmed"})
		return
	}
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

// Undelivered is what a bridge says of a message it could not deliver: the account it was for, a code (hub.Undelivered*: what happened,
// on any platform), and detail, the platform's own words, for the audit log.
type Undelivered struct {
	Account string `json:"account"`
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
}

// undelivered takes a bridge's word that a message the hub gave it did not reach the person. The agent is told.
func (s *Server) undelivered(w http.ResponseWriter, r *http.Request, bridge string) {
	var in Undelivered
	if err := json.NewDecoder(io.LimitReader(r.Body, maxIn)).Decode(&in); err != nil || !validAccount(in.Account) || in.Code == "" || len(in.Code) > 64 {
		answer(w, http.StatusBadRequest, map[string]string{"error": "expected {account, code, detail}"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), deliverLimit)
	defer cancel()
	err := s.hub.Undelivered(ctx, bridge, r.PathValue("id"), in.Account, in.Code, in.Detail)
	switch {
	case errors.Is(err, hub.ErrNotFound):
		answer(w, http.StatusNotFound, map[string]string{"error": "no such message for this bridge"})
	case err != nil:
		s.log.Warn("tell an agent that its message was not delivered", "bridge", bridge, "err", err)
		answer(w, http.StatusServiceUnavailable, map[string]string{"error": "the agent could not be told now"})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// wellFormed checks the shape of what a bridge posts; the hub checks the person.
func wellFormed(in Inbound) bool {
	return projectID.MatchString(in.Project) && corenames.Instance(in.Agent) && validAccount(in.Account) &&
		strings.TrimSpace(in.Text) != "" && printable(in.Key, maxKey)
}

// maxKey is the longest key a bridge gives: the hub puts the bridge's name (at most 32 characters) and a colon before it, and a project
// takes keys of up to 128.
const maxKey = 95

// validAccount is a platform's id for a person, as a bridge names it.
func validAccount(account string) bool { return printable(account, 64) }

// printable is an id a bridge gives, a person's account or a key: 1 to max characters, none a space or a control character.
func printable(id string, max int) bool {
	if id == "" || len(id) > max {
		return false
	}
	for _, r := range id {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// code makes a link code for a person's account: the bridge shows them the address, they confirm it signed in on the hub.
func (s *Server) code(w http.ResponseWriter, r *http.Request, bridge string) {
	var in struct {
		Account string `json:"account"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxIn)).Decode(&in); err != nil || !validAccount(in.Account) || len(in.Name) > 100 {
		answer(w, http.StatusBadRequest, map[string]string{"error": "expected {account, name}"})
		return
	}
	code, expires, err := s.hub.BridgeCode(r.Context(), bridge, in.Account, strings.TrimSpace(in.Name))
	if err != nil {
		s.log.Error("make a bridge code", "bridge", bridge, "err", err)
		answer(w, http.StatusInternalServerError, map[string]string{"error": "no code"})
		return
	}
	answer(w, http.StatusOK, map[string]any{"code": code, "url": s.public + LinkPath + "#" + code, "expires": expires.UTC()})
}

// Account is a linked account, as a bridge reads the list.
type Account struct {
	Account  string `json:"account"`
	Name     string `json:"name"`
	User     string `json:"user"` // the hub user id, as messages name it: user:<id>
	Username string `json:"username"`
}

// accounts lists the accounts linked through this bridge: whom user:<id> is on the platform.
func (s *Server) accounts(w http.ResponseWriter, r *http.Request, bridge string) {
	list, err := s.hub.BridgeAccounts(r.Context(), bridge)
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"error": "no list"})
		return
	}
	out := make([]Account, 0, len(list))
	for _, a := range list {
		out = append(out, Account{Account: a.ExternalID, Name: a.ExternalName, User: a.UserID, Username: a.Username})
	}
	answer(w, http.StatusOK, out)
}

// agents lists the agents the person behind ?account= may chat with.
func (s *Server) agents(w http.ResponseWriter, r *http.Request, bridge string) {
	account := r.URL.Query().Get("account")
	if !validAccount(account) {
		answer(w, http.StatusBadRequest, map[string]string{"error": "expected ?account="})
		return
	}
	list, err := s.hub.ChatAgents(r.Context(), bridge, account)
	if errors.Is(err, hub.ErrForbidden) {
		answer(w, http.StatusForbidden, map[string]string{"error": "not linked"})
		return
	} else if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"error": "no list"})
		return
	}
	answer(w, http.StatusOK, list)
}

// mayAdd says whether the person may add an agent to a place.
func (s *Server) mayAdd(w http.ResponseWriter, r *http.Request, bridge string) {
	var in struct {
		Account string `json:"account"`
		Project string `json:"project"`
		Agent   string `json:"agent"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxIn)).Decode(&in); err != nil || !validAccount(in.Account) || !projectID.MatchString(in.Project) || !corenames.Instance(in.Agent) {
		answer(w, http.StatusBadRequest, map[string]string{"error": "expected {account, project, agent}"})
		return
	}
	ok, err := s.hub.MayAdd(r.Context(), bridge, in.Account, in.Project, in.Agent)
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"error": "cannot say"})
		return
	}
	answer(w, http.StatusOK, map[string]bool{"allowed": ok})
}

// maxPresence is how many agents one presence call asks about: 256 KB of names at most.
const maxPresence = 1000

// presence says whether agents are still in their projects: a bridge keeps the places of an agent that left for a while, in case it comes
// back under the same name, and asks this to know when to let them go.
func (s *Server) presence(w http.ResponseWriter, r *http.Request, bridge string) {
	var in struct {
		Agents []hub.AgentRef `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil || len(in.Agents) > maxPresence {
		answer(w, http.StatusBadRequest, map[string]string{"error": "expected {agents: [{project, agent}]}, at most 1000"})
		return
	}
	for _, a := range in.Agents {
		if !projectID.MatchString(a.Project) || !corenames.Instance(a.Agent) {
			answer(w, http.StatusBadRequest, map[string]string{"error": "expected {agents: [{project, agent}]}, at most 1000"})
			return
		}
	}
	list, err := s.hub.AgentsPresent(r.Context(), in.Agents)
	if err != nil {
		s.log.Warn("say whether agents are present", "bridge", bridge, "err", err)
		answer(w, http.StatusInternalServerError, map[string]string{"error": "cannot say"})
		return
	}
	answer(w, http.StatusOK, map[string]any{"agents": list})
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
