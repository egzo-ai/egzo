// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/internal/ahp"
)

// conn is one client connection: what it is subscribed to, and what waits to be written to it.
type conn struct {
	mu       sync.Mutex
	subs     map[string]bool
	clientID string
	out      chan []byte
	closed   bool
	close    func()
}

func (c *conn) subscribed(channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[channel]
}

func (c *conn) subscribe(channels ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range channels {
		c.subs[ch] = true
	}
}

// send queues a message for the connection; one that cannot keep up is closed (it reconnects, and gets what it missed).
func (c *conn) send(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.out <- data:
	default:
		c.closed = true
		close(c.out)
		c.close()
	}
}

func (c *conn) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.out)
	}
}

// Handler serves the Host: AHP over a WebSocket at path, and the hooks at hookPath (POST, one event, its output as the answer). Both ask
// for the token, as a bearer token.
func (h *Host) Handler(token, path, hookPath string) http.Handler {
	authorized := func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) == 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(ahp.MaxMessage)
		h.serve(r.Context(), ws)
	})
	mux.HandleFunc(hookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			return
		}
		var event HookEvent
		if json.Unmarshal(body, &event) != nil {
			http.Error(w, "not a hook event", http.StatusBadRequest)
			return
		}
		out := h.Hook(event, r.Context().Done())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return mux
}

// Hook takes one hook event of Claude Code; its answer is what the hook prints (nil for nothing). Events of another session than the main
// one are not the chat's.
func (h *Host) Hook(event HookEvent, done <-chan struct{}) []byte {
	if event.SessionID != "" && event.SessionID != h.cfg.Session {
		return nil
	}
	if event.TranscriptPath != "" && h.onTranscript != nil {
		h.onTranscript(event.TranscriptPath)
	}
	switch event.Event {
	case "SessionStart":
		h.Started(event.Source)
	case "Stop":
		h.Stopped("")
	case "StopFailure":
		message := event.ErrorDetails
		if message == "" {
			var s string
			if json.Unmarshal(event.Error, &s) == nil {
				message = s
			} else {
				message = string(event.Error)
			}
		}
		if message == "" {
			message = "Claude Code stopped on an error"
		}
		h.Stopped(message)
	case "PermissionRequest":
		return h.Permission(event, done)
	}
	return nil
}

// serve is one client's connection until it closes.
func (h *Host) serve(ctx context.Context, ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &conn{subs: map[string]bool{}, out: make(chan []byte, 4096), close: cancel}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, c)
		h.mu.Unlock()
		c.finish()
		ws.CloseNow()
	}()
	go func() {
		for data := range c.out {
			if ws.Write(ctx, websocket.MessageText, data) != nil {
				cancel()
				return
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		h.request(c, data)
	}
}

// request answers one JSON-RPC message of a client.
func (h *Host) request(c *conn, data []byte) {
	var m struct {
		ID     json.RawMessage            `json:"id"`
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if json.Unmarshal(data, &m) != nil || m.Method == "" {
		return
	}
	str := func(name string) string {
		var s string
		_ = json.Unmarshal(m.Params[name], &s)
		return s
	}
	answer := func(result any) {
		if len(m.ID) > 0 && string(m.ID) != "null" {
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			c.send(out)
		}
	}
	fail := func(code int, message string) {
		if len(m.ID) > 0 && string(m.ID) != "null" {
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": code, "message": message}})
			c.send(out)
		}
	}
	// the answer is queued under the Host's lock, so no action published after the state it describes can overtake it
	h.mu.Lock()
	defer h.mu.Unlock()
	switch m.Method {
	case "initialize":
		var initial []string
		_ = json.Unmarshal(m.Params["initialSubscriptions"], &initial)
		c.clientID = str("clientId")
		h.clients[c.clientID] = true
		snapshots := []any{}
		for _, uri := range append([]string{ahp.RootChannel}, initial...) {
			if snap := h.snapshotLocked(uri); snap != nil {
				c.subscribe(uri)
				if uri != ahp.RootChannel || contains(initial, uri) {
					snapshots = append(snapshots, snap)
				}
			}
		}
		answer(map[string]any{"protocolVersion": "1.0.0", "serverSeq": h.seq,
			"serverInfo": map[string]any{"name": "egzo-claude-ahp", "version": h.cfg.Version}, "snapshots": snapshots})
	case "reconnect":
		var subs []string
		var last int
		_ = json.Unmarshal(m.Params["subscriptions"], &subs)
		_ = json.Unmarshal(m.Params["lastSeenServerSeq"], &last)
		c.clientID = str("clientId")
		known := h.clients[c.clientID]
		h.clients[c.clientID] = true
		served := []string{}
		for _, uri := range subs {
			if h.snapshotLocked(uri) != nil {
				served = append(served, uri)
			}
		}
		c.subscribe(served...)
		// what it missed, when the Host still has all of it
		if known && last <= h.seq && (len(h.log) == 0 || h.log[0].ServerSeq <= last+1) {
			actions := []envelope{}
			for _, e := range h.log {
				if e.ServerSeq > last && c.subscribed(e.Channel) {
					actions = append(actions, e)
				}
			}
			answer(map[string]any{"type": "replay", "actions": actions, "missing": []string{}})
			return
		}
		snapshots := []any{}
		for _, uri := range served {
			snapshots = append(snapshots, h.snapshotLocked(uri))
		}
		answer(map[string]any{"type": "snapshot", "snapshots": snapshots})
	case "ping":
		answer(map[string]any{})
	case "listSessions":
		answer(map[string]any{"items": []any{h.summaryLocked()}})
	case "subscribe":
		channel := str("channel")
		snap := h.snapshotLocked(channel)
		if snap == nil {
			fail(-32003, "cannot subscribe to "+channel)
			return
		}
		c.subscribe(channel)
		answer(map[string]any{"snapshot": snap})
	case "unsubscribe":
		c.mu.Lock()
		delete(c.subs, str("channel"))
		c.mu.Unlock()
		answer(map[string]any{})
	case "fetchTurns":
		// every snapshot holds every turn: there is never an older page
		if str("channel") != h.main.Chat {
			fail(-32602, "no such chat")
			return
		}
		if _, given := m.Params["cursor"]; given {
			fail(-32602, "unknown cursor")
			return
		}
		answer(map[string]any{})
	case "dispatchAction":
		if str("channel") != h.main.Chat {
			fail(-32602, "only the main session's chat takes actions")
			return
		}
		var action map[string]any
		var clientSeq any
		_ = json.Unmarshal(m.Params["action"], &action)
		_ = json.Unmarshal(m.Params["clientSeq"], &clientSeq)
		if action == nil {
			fail(-32602, "no action")
			return
		}
		origin := map[string]any{"clientId": c.clientID, "clientSeq": clientSeq}
		h.mu.Unlock()
		h.Dispatch(action, origin)
		h.mu.Lock()
		answer(map[string]any{})
	default:
		fail(-32601, m.Method+" is not supported by Claude Code's adapter")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
