package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/store"
)

// maxChatMessage is the longest message of the Rich UI chat either way: a chat's snapshot holds its turns, which can be long.
const maxChatMessage = 16 << 20

// richChat bridges the browser's Rich UI to the chat of an agent spawned in AHP mode: each text frame is one AHP message, JSON-RPC, both
// ways. The link lets through only the chat of the agent's main session (hublink.VerbAHP). The hub's own first frame says whether this person
// only watches: {"jsonrpc":"2.0","method":"x-egzo/chatOpened","params":{"readOnly":true}}.
func (s *Server) richChat(w http.ResponseWriter, r *http.Request, user store.User) {
	// As for the terminal: only a WebSocket from the hub's own pages says who it comes from.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Origin") != s.urls.Base(r) {
		s.apiFail(w, http.StatusForbidden, "forbidden", "Cross-site request refused.")
		return
	}
	cookie, err := s.readCookie(r, sessionCookie)
	var expires time.Time
	if err == nil {
		expires, err = s.hub.Store().SessionExpires(r.Context(), cookie.Value)
	}
	if err != nil {
		s.apiFail(w, http.StatusUnauthorized, "unauthorized", "Sign in again.")
		return
	}
	chat, err := s.hub.OpenChat(r.Context(), user, r.PathValue("project"), r.PathValue("agent"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	base, _ := url.Parse(s.urls.Base(r))
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{base.Host}})
	if err != nil {
		return // Accept has answered
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxChatMessage)
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	// the chat belongs to the session that opened it, like a terminal: signing out, being disabled or unlinking ends it
	defer s.trackTerminal(store.HashToken(cookie.Value), cancel)()

	opened, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "x-egzo/chatOpened", "params": map[string]bool{"readOnly": chat.ReadOnly}})
	if conn.Write(ctx, websocket.MessageText, opened) != nil {
		return
	}
	err = chat.Run(ctx, func(link net.Conn) error {
		done := make(chan struct{})
		// browser -> agent: one frame, one line
		go func() {
			defer close(done)
			defer cancel()
			for {
				kind, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				if kind != websocket.MessageText || !json.Valid(data) {
					continue
				}
				var line bytes.Buffer
				_ = json.Compact(&line, data)
				line.WriteByte('\n')
				if _, err := link.Write(line.Bytes()); err != nil {
					return
				}
			}
		}()
		// agent -> browser: one line, one frame
		scanner := bufio.NewScanner(link)
		scanner.Buffer(make([]byte, 64<<10), maxChatMessage)
		for scanner.Scan() {
			if err := conn.Write(ctx, websocket.MessageText, scanner.Bytes()); err != nil {
				break
			}
		}
		cancel()
		<-done
		return scanner.Err()
	})
	if err != nil && ctx.Err() == nil {
		failed, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "x-egzo/chatError", "params": map[string]string{"message": err.Error()}})
		_ = conn.Write(context.Background(), websocket.MessageText, failed)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// chatMessage is a message a person sent the agent, which its chat names in egzo's line announcing it.
func (s *Server) chatMessage(w http.ResponseWriter, r *http.Request, user store.User) {
	m, err := s.hub.ChatMessageOf(r.Context(), user, r.PathValue("project"), r.PathValue("agent"), r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}
