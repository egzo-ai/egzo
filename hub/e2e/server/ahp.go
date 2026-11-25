package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/internal/ahp"
)

// scriptedAHP stands in for the AHP server of an agent spawned in AHP mode (opencode-ahp inside OpenCode), with `egzo agent ahp` and the
// link's filter in front of it: what the hub reaches through the verb ahp. It speaks the same actions as the plugin does, a turn at a time,
// and every connection subscribed to the chat sees the same chat. What a prompt says decides the turn: one that says "wait" runs until it is
// cancelled, one that says "fail" ends in an error; any other reads a file, thinks, and answers in a few deltas.
type scriptedAHP struct {
	mu    sync.Mutex
	main  ahp.MainSession
	turns []map[string]any      // finished turns, oldest first, as a snapshot holds them
	live  map[string]any        // the turn that runs, if any
	conns map[*ahpConn]struct{} // the connections subscribed to the chat
	heard []json.RawMessage     // every line a client sent that the filter let through, for the tests
}

type ahpConn struct {
	mu  sync.Mutex
	out net.Conn
}

func (c *ahpConn) send(v any) {
	data, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.out.Write(append(data, '\n'))
}

func newScriptedAHP(session string) *scriptedAHP {
	return &scriptedAHP{
		main:  ahp.SessionURIs(session),
		conns: map[*ahpConn]struct{}{},
		// what the agent did before the page was opened: a turn the chat's history holds
		turns: []map[string]any{{
			"id": "turn-before", "message": map[string]any{"text": "Explain the parser", "origin": map[string]any{"kind": "user"}}, "state": "complete",
			"responseParts": []any{map[string]any{"kind": "markdown", "id": "m0", "content": "It is a recursive descent parser."}},
		}},
	}
}

// serve is one connection the hub dialed: lines from the hub go through the filter to the server, lines from the server through the filter
// back, as the link does around `egzo agent ahp`.
func (s *scriptedAHP) serve(conn net.Conn, readOnly bool) {
	defer conn.Close()
	filter := &ahp.Filter{ReadOnly: readOnly}
	out := &ahpConn{out: conn}
	// what the server says goes through the filter first
	server := func(v any) {
		data, _ := json.Marshal(v)
		if passed := filter.FromServer(data); passed != nil {
			out.mu.Lock()
			_, _ = conn.Write(append(passed, '\n'))
			out.mu.Unlock()
		}
	}
	filtered := &ahpConn{out: &filterWriter{conn: conn, filter: filter, mu: &out.mu}}
	defer func() {
		s.mu.Lock()
		delete(s.conns, filtered)
		s.mu.Unlock()
	}()
	// the bridge says which session is the agent's main one as soon as it knows it
	server(map[string]any{"jsonrpc": "2.0", "method": ahp.MainSessionMethod, "params": s.main})

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64<<10), ahp.MaxMessage)
	for scanner.Scan() {
		forward, reply := filter.FromClient(scanner.Bytes())
		if reply != nil {
			out.mu.Lock()
			_, _ = conn.Write(append(reply, '\n'))
			out.mu.Unlock()
		}
		if forward == nil {
			continue
		}
		var m struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(forward, &m)
		s.mu.Lock()
		s.heard = append(s.heard, append(json.RawMessage{}, forward...))
		s.mu.Unlock()
		answer := func(result any) {
			if len(m.ID) > 0 {
				server(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			}
		}
		switch m.Method {
		case "initialize":
			answer(map[string]any{"protocolVersion": "1.0.0", "serverInfo": map[string]any{"name": "scripted-ahp", "version": "1"}, "snapshots": []any{}})
		case "ping":
			answer(map[string]any{})
		case "listSessions":
			// every session of the server: the filter keeps the main one
			answer(map[string]any{"items": []any{
				map[string]any{"resource": s.main.Session, "title": "the main session", "defaultChat": s.main.Chat},
				map[string]any{"resource": "ahp-session:/ses_other", "title": "a session opened with /new"},
			}})
		case "subscribe":
			var channel string
			_ = json.Unmarshal(m.Params["channel"], &channel)
			s.mu.Lock()
			if channel == s.main.Chat {
				s.conns[filtered] = struct{}{}
			}
			state := map[string]any{"resource": s.main.Chat, "title": "the main session", "turns": append([]map[string]any{}, s.turns...)}
			if s.live != nil {
				state["activeTurn"] = s.live
			}
			s.mu.Unlock()
			answer(map[string]any{"snapshot": map[string]any{"resource": channel, "state": state, "fromSeq": 0}})
		case "unsubscribe":
			s.mu.Lock()
			delete(s.conns, filtered)
			s.mu.Unlock()
			answer(map[string]any{})
		case "dispatchAction":
			var action map[string]any
			_ = json.Unmarshal(m.Params["action"], &action)
			s.dispatch(action)
		}
	}
}

// filterWriter is a connection's way to the hub for what the server broadcasts: through the connection's filter, like the rest.
type filterWriter struct {
	net.Conn
	conn   net.Conn
	filter *ahp.Filter
	mu     *sync.Mutex
}

func (w *filterWriter) Write(line []byte) (int, error) {
	passed := w.filter.FromServer([]byte(strings.TrimSpace(string(line))))
	if passed == nil {
		return len(line), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.conn.Write(append(passed, '\n'))
	return len(line), err
}

// broadcast sends an action of the chat to every connection subscribed to it.
func (s *scriptedAHP) broadcast(action map[string]any) {
	s.mu.Lock()
	conns := make([]*ahpConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	channel := s.main.Chat
	s.mu.Unlock()
	for _, c := range conns {
		c.send(map[string]any{"jsonrpc": "2.0", "method": "action", "params": map[string]any{"channel": channel, "action": action}})
	}
}

func (s *scriptedAHP) dispatch(action map[string]any) {
	switch action["type"] {
	case "chat/turnStarted":
		id, _ := action["turnId"].(string)
		message, _ := action["message"].(map[string]any)
		text, _ := message["text"].(string)
		s.start(id, text)
	case "chat/turnCancelled":
		id, _ := action["turnId"].(string)
		s.finish(id, "cancelled", map[string]any{"type": "chat/turnCancelled", "turnId": id, "duration": 1})
	}
}

// say is a prompt typed into the agent's terminal (a person, or a message egzo typed): the plugin mirrors its turn into the chat.
func (s *scriptedAHP) say(text string) {
	s.start(fmt.Sprintf("turn-tui-%d", time.Now().UnixNano()), text)
}

func (s *scriptedAHP) start(id, text string) {
	s.mu.Lock()
	if s.live != nil {
		s.mu.Unlock()
		return // one turn at a time, as OpenCode
	}
	s.live = map[string]any{"id": id, "message": map[string]any{"text": text, "origin": map[string]any{"kind": "user"}}, "responseParts": []any{},
		"startedAt": time.Now().UTC().Format(time.RFC3339)}
	s.mu.Unlock()
	s.broadcast(map[string]any{"type": "chat/turnStarted", "turnId": id, "message": map[string]any{"text": text, "origin": map[string]any{"kind": "user"}}})
	go s.run(id, text)
}

func (s *scriptedAHP) running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live != nil && s.live["id"] == id
}

// run plays a turn the way OpenCode's events become actions in the plugin.
func (s *scriptedAHP) run(id, text string) {
	step := func(action map[string]any) bool {
		time.Sleep(150 * time.Millisecond)
		if !s.running(id) {
			return false
		}
		action["turnId"] = id
		s.broadcast(action)
		return true
	}
	if !step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "reasoning", "id": "r1", "content": "Looking at the "}}) ||
		!step(map[string]any{"type": "chat/reasoning", "partId": "r1", "content": "repository first."}) ||
		!step(map[string]any{"type": "chat/toolCallStart", "toolCallId": "call-1", "toolName": "read", "displayName": "Read"}) ||
		!step(map[string]any{"type": "chat/toolCallReady", "toolCallId": "call-1", "invocationMessage": "README.md", "confirmed": "not-needed"}) {
		return
	}
	if strings.Contains(text, "wait") {
		return // runs until someone stops it
	}
	if strings.Contains(text, "fail") {
		s.finish(id, "error", map[string]any{"type": "chat/error", "turnId": id, "duration": 1,
			"part": map[string]any{"kind": "error", "error": map[string]any{"errorType": "APIError", "message": "the model refused the request"}}})
		return
	}
	if !step(map[string]any{"type": "chat/toolCallComplete", "toolCallId": "call-1", "result": map[string]any{"success": true, "pastTenseMessage": "Read README.md"}}) ||
		!step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "markdown", "id": "m1", "content": "The README "}}) ||
		!step(map[string]any{"type": "chat/delta", "partId": "m1", "content": "says how to build "}) ||
		!step(map[string]any{"type": "chat/delta", "partId": "m1", "content": "the project."}) {
		return
	}
	time.Sleep(150 * time.Millisecond)
	s.finish(id, "complete", map[string]any{"type": "chat/turnComplete", "turnId": id, "duration": 1})
}

// finish ends the running turn id: it joins the history, and its last action goes out.
func (s *scriptedAHP) finish(id, state string, last map[string]any) {
	s.mu.Lock()
	if s.live == nil || s.live["id"] != id {
		s.mu.Unlock()
		return
	}
	s.live["state"] = state
	s.turns = append(s.turns, s.live)
	s.live = nil
	s.mu.Unlock()
	s.broadcast(last)
}

// heardLines is what clients sent the server, after the filter.
func (s *scriptedAHP) heardLines() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage{}, s.heard...)
}
