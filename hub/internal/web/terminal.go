package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// control is a text frame: the browser tells the size of its terminal, the hub tells whether the keyboard is the user's.
type control struct {
	Type     string `json:"type"`
	Cols     uint   `json:"cols,omitempty"`
	Rows     uint   `json:"rows,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	Message  string `json:"message,omitempty"`
}

// terminal bridges a browser's xterm.js to an agent's session: binary frames are the terminal's bytes in both directions, text
// frames are control messages.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, agent := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent")
	att, err := s.hub.Attach(r.Context(), user, engine, project, agent)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer att.Close()
	base, _ := url.Parse(s.urls.Base(r))
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{base.Host}})
	if err != nil {
		return // Accept has answered
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sendControl := func(c control) {
		data, _ := json.Marshal(c)
		_ = conn.Write(ctx, websocket.MessageText, data)
	}
	sendControl(control{Type: "mode", ReadOnly: att.ReadOnly})

	input, inputWriter := io.Pipe()
	resize := make(chan engines.Size, 8)
	size := engines.Size{Cols: 80, Rows: 24}
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
						size = engines.Size{Cols: c.Cols, Rows: c.Rows}
						first = false
						close(sized)
					} else {
						select {
						case resize <- engines.Size{Cols: c.Cols, Rows: c.Rows}:
						default:
						}
					}
				}
				continue
			}
			if !att.ReadOnly {
				if _, err := inputWriter.Write(data); err != nil {
					return
				}
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
	err = att.Run(ctx, engines.Terminal{In: input, Out: out, Size: size, Resize: resize})
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		conn.Close(websocket.StatusNormalClosure, "the session ended")
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
