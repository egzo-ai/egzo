package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// control is a text frame: the browser tells the size of its terminal; the hub says when the terminal is in use by somebody else ("busy"),
// was taken over ("taken") or failed ("error").
type control struct {
	Type    string `json:"type"`
	Cols    uint   `json:"cols,omitempty"`
	Rows    uint   `json:"rows,omitempty"`
	Holder  string `json:"holder,omitempty"`
	Message string `json:"message,omitempty"`
}

// terminal bridges a browser's xterm.js to an agent's session: binary frames are the terminal's bytes in both directions, text
// frames are control messages.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request, user store.User) {
	// Attaching takes the seat and may end somebody's terminal, so it comes after this. A GET is not held to sameOrigin, and a link followed
	// from another site brings the cookie: only a WebSocket from the hub's own pages says who it comes from.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Origin") != s.urls.Base(r) {
		s.apiFail(w, http.StatusForbidden, "forbidden", "Cross-site request refused.")
		return
	}
	// a terminal is not a request that ends when it has been answered: it ends with the session, like everything the session gave
	cookie, err := s.readCookie(r, sessionCookie)
	var expires time.Time
	if err == nil {
		expires, err = s.hub.Store().SessionExpires(r.Context(), cookie.Value)
	}
	if err != nil {
		s.apiFail(w, http.StatusUnauthorized, "unauthorized", "Sign in again.")
		return
	}
	project, agent := r.PathValue("project"), r.PathValue("agent")
	att, err := s.hub.Attach(r.Context(), user, project, agent, r.URL.Query().Get("takeover") == "1")
	var inUse *hub.InUseError
	if err != nil && !errors.As(err, &inUse) {
		s.problem(w, r, err)
		return
	}
	base, _ := url.Parse(s.urls.Base(r))
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{base.Host}})
	if err != nil {
		if att != nil {
			att.Close()
		}
		return // Accept has answered
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	// the terminal belongs to the session that opened it: signing out, being disabled or unlinking ends it
	defer s.trackTerminal(store.HashToken(cookie.Value), cancel)()

	sendControl := func(c control) {
		data, _ := json.Marshal(c)
		_ = conn.Write(ctx, websocket.MessageText, data)
	}
	if inUse != nil {
		sendControl(control{Type: "busy", Holder: inUse.Holder})
		conn.Close(websocket.StatusNormalClosure, "the terminal is in use")
		return
	}
	defer att.Close()

	input, inputWriter := io.Pipe()
	resize := make(chan links.Size, 8)
	size := links.Size{Cols: 80, Rows: 24}
	sized := make(chan struct{})

	// browser -> agent
	go func() {
		defer inputWriter.Close()
		defer cancel()
		first := true
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				var c control
				if json.Unmarshal(data, &c) == nil && c.Type == "resize" && c.Cols > 0 && c.Rows > 0 && c.Cols < 1000 && c.Rows < 1000 {
					if first {
						size = links.Size{Cols: c.Cols, Rows: c.Rows}
						first = false
						close(sized)
					} else {
						select {
						case resize <- links.Size{Cols: c.Cols, Rows: c.Rows}:
						default:
						}
					}
				}
				continue
			}
			if _, err := inputWriter.Write(data); err != nil {
				return
			}
		}
	}()

	// the first size decides how the session is attached; a browser that never says gets 80x24
	select {
	case <-sized:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return
	}

	out := frameWriter{ctx: ctx, conn: conn}
	err = att.Run(ctx, links.Terminal{In: input, Out: out, Size: size, Resize: resize})
	select {
	case <-att.Taken():
		sendControl(control{Type: "taken", Holder: att.TakenBy()})
		conn.Close(websocket.StatusNormalClosure, "taken over")
		return
	case <-att.Revoked():
		sendControl(control{Type: "error", Message: "Your role on this project changed, and this terminal is no longer yours. The agent's session goes on."})
		conn.Close(websocket.StatusNormalClosure, "access changed")
		return
	default:
	}
	switch {
	case err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		conn.Close(websocket.StatusNormalClosure, "the session ended")
	case errors.As(err, new(hub.OfflineError)):
		sendControl(control{Type: "error", Message: "The link to this project dropped, and the terminal with it. The agent's session goes on."})
		conn.Close(websocket.StatusNormalClosure, "the link dropped")
	default:
		s.log.Warn("terminal", "agent", agent, "err", err)
		sendControl(control{Type: "error", Message: err.Error()})
		conn.Close(websocket.StatusInternalError, "attach failed")
	}
}

type frameWriter struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (f frameWriter) Write(p []byte) (int, error) {
	if err := f.conn.Write(f.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
