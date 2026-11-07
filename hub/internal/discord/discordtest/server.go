// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package discordtest is a fake Discord: the gateway and the REST API the bridge uses, with what a test needs to play people and
// Discord's own trouble. It keeps every event of a session, so a client that resumes is sent what it missed, and records every
// request.
package discordtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Request is a REST request the fake got.
type Request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
	Response     []byte // what the fake answered
	answered     bool
}

// JSON decodes the request's body.
func (r Request) JSON(v any) error { return json.Unmarshal(r.Body, v) }

type event struct {
	seq int64
	t   string
	d   json.RawMessage
}

type session struct {
	id     string
	events []event // everything dispatched in the session, for a resume
	conn   *websocket.Conn
	zombie bool // heartbeats are not acknowledged and nothing is sent: events wait for a resume
}

// Server is a fake Discord.
type Server struct {
	Token     string
	Heartbeat time.Duration // what hello says; short in tests
	srv       *httptest.Server
	nextID    atomic.Int64

	mu         sync.Mutex
	sessions   map[string]*session
	current    *session
	requests   []Request
	limited    map[string]int // path prefix -> how many answers are 429 still
	identifies int
	resumes    int
	connected  chan struct{} // closed and made anew at every READY or RESUMED
	ready      []json.RawMessage
	starts     int           // session starts left; negative: plenty
	resetAfter time.Duration // when they renew
}

// SessionStarts sets how many sessions the bot may still start, and when that renews.
func (s *Server) SessionStarts(left int, resetAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts, s.resetAfter = left, resetAfter
}

// New starts a fake Discord for a bot token.
func New(token string) *Server {
	s := &Server{Token: token, Heartbeat: time.Second, sessions: map[string]*session{}, limited: map[string]int{}, connected: make(chan struct{}), starts: -1}
	s.nextID.Store(1000)
	mux := http.NewServeMux()
	mux.HandleFunc("/gateway/", s.gateway)
	mux.HandleFunc("/api/v10/", s.rest)
	s.srv = httptest.NewServer(mux)
	return s
}

// Close stops it.
func (s *Server) Close() {
	s.mu.Lock()
	if s.current != nil && s.current.conn != nil {
		s.current.conn.CloseNow()
	}
	s.mu.Unlock()
	s.srv.Close()
}

// API is where the REST API is, for discord.NewREST.
func (s *Server) API() string { return s.srv.URL + "/api/v10" }

// GatewayURL is where the gateway is.
func (s *Server) GatewayURL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/gateway"
}

// ID makes a snowflake.
func (s *Server) ID() string { return fmt.Sprint(s.nextID.Add(1)) }

// OnReady sends these events after every READY of a new session: the guilds, as GUILD_CREATE data.
func (s *Server) OnReady(guilds ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range guilds {
		data, _ := json.Marshal(g)
		s.ready = append(s.ready, data)
	}
}

// Connected is closed at the next READY or RESUMED.
func (s *Server) Connected() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Counts says how many times a client identified and resumed.
func (s *Server) Counts() (identifies, resumes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identifies, s.resumes
}

// Dispatch sends an event to the session, or keeps it for the resume when the connection is gone.
func (s *Server) Dispatch(t string, data any) {
	raw, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return
	}
	s.dispatchLocked(s.current, t, raw)
}

func (s *Server) dispatchLocked(sess *session, t string, raw json.RawMessage) {
	e := event{seq: int64(len(sess.events) + 1), t: t, d: raw}
	sess.events = append(sess.events, e)
	if sess.conn != nil && !sess.zombie {
		write(sess.conn, map[string]any{"op": 0, "s": e.seq, "t": e.t, "d": e.d})
	}
}

// Drop cuts the connection with no close, as a network would.
func (s *Server) Drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.conn != nil {
		s.current.conn.CloseNow()
		s.current.conn = nil
	}
}

// Zombie keeps the connection open and stops answering on it: no heartbeat ack, no event.
func (s *Server) Zombie() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.zombie = true
	}
}

// Reconnect asks the client to reconnect and resume (opcode 7).
func (s *Server) Reconnect() { s.op(map[string]any{"op": 7, "d": nil}) }

// InvalidSession says the session is invalid (opcode 9); resumable says whether it may be resumed.
func (s *Server) InvalidSession(resumable bool) {
	s.mu.Lock()
	if !resumable && s.current != nil {
		delete(s.sessions, s.current.id)
	}
	s.mu.Unlock()
	s.op(map[string]any{"op": 9, "d": resumable})
}

// CloseWith closes the connection with a code, such as 4004 for a wrong token.
func (s *Server) CloseWith(code int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.conn != nil {
		s.current.conn.Close(websocket.StatusCode(code), reason)
	}
}

func (s *Server) op(p any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.conn != nil {
		write(s.current.conn, p)
	}
}

// RateLimit makes the next n requests to a path answer 429.
func (s *Server) RateLimit(path string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limited[path] = n
}

// Requests lists the REST requests answered so far, with prefix as the start of their "METHOD path" ("" for all).
func (s *Server) Requests(prefix string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests {
		if r.answered && strings.HasPrefix(r.Method+" "+r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func write(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, data)
}

// gateway is one connection: hello, then identify or resume, then heartbeats until it ends.
func (s *Server) gateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	write(conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": s.Heartbeat.Milliseconds()}})
	var mine *session
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var p struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
		}
		if json.Unmarshal(data, &p) != nil {
			conn.Close(4002, "decode error")
			return
		}
		switch p.Op {
		case 1: // heartbeat
			s.mu.Lock()
			zombie := mine != nil && mine.zombie
			s.mu.Unlock()
			if !zombie {
				write(conn, map[string]any{"op": 11})
			}
		case 2: // identify
			var id struct {
				Token   string `json:"token"`
				Intents int    `json:"intents"`
			}
			_ = json.Unmarshal(p.D, &id)
			if id.Token != s.Token {
				conn.Close(4004, "Authentication failed.")
				return
			}
			s.mu.Lock()
			s.identifies++
			mine = &session{id: s.ID(), conn: conn}
			s.sessions[mine.id] = mine
			s.current = mine
			ready, _ := json.Marshal(map[string]any{"v": 10, "session_id": mine.id, "resume_gateway_url": s.GatewayURL(),
				"user": map[string]any{"id": "1", "username": "egzo", "bot": true}, "application": map[string]any{"id": "2"}, "guilds": []any{}})
			s.dispatchLocked(mine, "READY", ready)
			for _, g := range s.ready {
				s.dispatchLocked(mine, "GUILD_CREATE", g)
			}
			s.connectedLocked()
			s.mu.Unlock()
		case 6: // resume
			var res struct {
				Token     string `json:"token"`
				SessionID string `json:"session_id"`
				Seq       int64  `json:"seq"`
			}
			_ = json.Unmarshal(p.D, &res)
			s.mu.Lock()
			old := s.sessions[res.SessionID]
			if res.Token != s.Token || old == nil {
				s.mu.Unlock()
				write(conn, map[string]any{"op": 9, "d": false})
				continue
			}
			s.resumes++
			mine = old
			mine.conn, mine.zombie = conn, false
			s.current = mine
			for _, e := range mine.events {
				if e.seq > res.Seq {
					write(conn, map[string]any{"op": 0, "s": e.seq, "t": e.t, "d": e.d})
				}
			}
			e := event{seq: int64(len(mine.events) + 1), t: "RESUMED", d: json.RawMessage(`{}`)}
			mine.events = append(mine.events, e)
			write(conn, map[string]any{"op": 0, "s": e.seq, "t": e.t, "d": e.d})
			s.connectedLocked()
			s.mu.Unlock()
		}
	}
}

func (s *Server) connectedLocked() {
	close(s.connected)
	s.connected = make(chan struct{})
}

// rest records a request and answers it: 429 when the path is limited, the gateway's address, or the body back with an id.
func (s *Server) rest(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/api/v10")
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: path, Header: r.Header.Clone(), Body: body})
	index := len(s.requests) - 1
	// a request is listed once it is answered: a test reads what the fake said
	defer func() {
		s.mu.Lock()
		s.requests[index].answered = true
		s.mu.Unlock()
	}()
	limited := false
	for prefix, n := range s.limited {
		if n > 0 && strings.HasPrefix(path, prefix) {
			s.limited[prefix] = n - 1
			limited = true
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasPrefix(path, "/interactions/") && !strings.HasPrefix(path, "/webhooks/") && r.Header.Get("Authorization") != "Bot "+s.Token {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message": "401: Unauthorized", "code": 0}`)
		return
	}
	if limited {
		w.Header().Set("Retry-After", "0.05")
		w.Header().Set("X-RateLimit-Scope", "user")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message": "You are being rate limited.", "retry_after": 0.05, "global": false}`)
		return
	}
	w.Header().Set("X-RateLimit-Limit", "5")
	w.Header().Set("X-RateLimit-Remaining", "4")
	w.Header().Set("X-RateLimit-Reset-After", "1")
	switch {
	case path == "/gateway/bot":
		s.mu.Lock()
		left, reset := s.starts, s.resetAfter
		s.mu.Unlock()
		if left < 0 {
			left = 999
		}
		json.NewEncoder(w).Encode(map[string]any{"url": s.GatewayURL(), "shards": 1,
			"session_start_limit": map[string]int64{"total": 1000, "remaining": int64(left), "reset_after": reset.Milliseconds(), "max_concurrency": 1}})
	case path == "/applications/@me":
		io.WriteString(w, `{"id": "2", "name": "egzo"}`)
	case r.Method == http.MethodDelete || strings.HasSuffix(path, "/callback") || r.Method == http.MethodPut:
		w.WriteHeader(http.StatusNoContent)
	default:
		out := map[string]any{}
		_ = json.Unmarshal(body, &out)
		out["id"] = s.ID()
		if strings.HasPrefix(path, "/channels/") {
			out["channel_id"] = strings.Split(path, "/")[2]
		}
		data, _ := json.Marshal(out)
		s.mu.Lock()
		s.requests[index].Response = data
		s.mu.Unlock()
		w.Write(data)
	}
}
