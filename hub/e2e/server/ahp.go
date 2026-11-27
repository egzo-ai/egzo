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
// link's filter in front of it: what the hub reaches through the verb ahp. It speaks as the plugin does: every action goes out in an
// envelope with its serverSeq, to every connection subscribed to its channel; a client that reconnects with its clientId gets what it
// missed replayed; the main session's channel carries its title. A dispatched action the server does not take is echoed with a
// rejectionReason. What a prompt says decides the turn: one that says "wait" runs until it is cancelled, one that says "fail" ends in an
// error, one that says "test" runs a shell command; any other reads a file, thinks, and answers in Markdown, in a few deltas.
type scriptedAHP struct {
	mu      sync.Mutex
	main    ahp.MainSession
	title   string
	turns   []map[string]any      // finished turns, oldest first, as a snapshot holds them
	live    map[string]any        // the turn that runs, if any
	pending []map[string]any      // the messages for the turn that runs: {id, kind, message}
	conns   map[*ahpConn]struct{} // the connections
	seq     int                   // the last serverSeq
	log     []envelope            // every action sent, for replays
	clients map[string]bool       // the clientIds that initialized or reconnected
	heard   []json.RawMessage     // every line a client sent that the filter let through, for the tests
	// refuse is how many subscribes to come are refused as the plugin refuses a session it has not registered yet: it registers a session
	// OpenCode made (the TUI's) 250 ms after the fact, and egzo may name it as the main one before that.
	refuse int
	// reject is why the next dispatched action is rejected, if it is
	reject string
	// asked are the questions and the tool approvals the running turn waits for, by request or tool call id: what a client answers goes
	// there (the chat/inputCompleted or chat/toolCallConfirmed it dispatched)
	asked map[string]chan map[string]any
}

type envelope struct {
	Channel         string         `json:"channel"`
	Action          map[string]any `json:"action"`
	ServerSeq       int            `json:"serverSeq"`
	Origin          map[string]any `json:"origin,omitempty"`
	RejectionReason string         `json:"rejectionReason,omitempty"`
}

// ahpConn is one connection the hub dialed: what it is subscribed to, and its way back through the link's filter.
type ahpConn struct {
	mu     sync.Mutex
	out    net.Conn
	filter *ahp.Filter
	subs   map[string]bool
}

// send writes what the server says to the connection, through the filter, as the link does.
func (c *ahpConn) send(v any) {
	data, _ := json.Marshal(v)
	passed := c.filter.FromServer(data)
	if passed == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.out.Write(append(passed, '\n'))
}

func (c *ahpConn) reply(line []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.out.Write(append(line, '\n'))
}

func newScriptedAHP(session string) *scriptedAHP {
	return &scriptedAHP{
		main:    ahp.SessionURIs(session),
		title:   "Explain the parser",
		conns:   map[*ahpConn]struct{}{},
		clients: map[string]bool{},
		asked:   map[string]chan map[string]any{},
		// what the agent did before the page was opened: a turn the chat's history holds
		turns: []map[string]any{{
			"id": "turn-before", "message": map[string]any{"text": "Explain the parser", "origin": map[string]any{"kind": "user"}}, "state": "complete",
			"startedAt": "2026-11-25T10:00:00Z", "duration": 3200,
			"responseParts": []any{map[string]any{"kind": "markdown", "id": "m0", "content": "It is a **recursive descent** parser."}},
			"usage":         map[string]any{"inputTokens": 5400, "outputTokens": 120, "model": "opencode-go/glm-5.3-flash"},
		}},
	}
}

// serve is one connection the hub dialed: lines from the hub go through the filter to the server, lines from the server through the filter
// back, as the link does around `egzo agent ahp`.
func (s *scriptedAHP) serve(conn net.Conn, readOnly bool) {
	defer conn.Close()
	c := &ahpConn{out: conn, filter: &ahp.Filter{ReadOnly: readOnly}, subs: map[string]bool{}}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	// the bridge says which session is the agent's main one as soon as it knows it
	c.send(map[string]any{"jsonrpc": "2.0", "method": ahp.MainSessionMethod, "params": s.main})

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64<<10), ahp.MaxMessage)
	for scanner.Scan() {
		forward, reply := c.filter.FromClient(scanner.Bytes())
		if reply != nil {
			c.reply(reply)
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
				c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			}
		}
		fail := func(code int, message string) {
			if len(m.ID) > 0 {
				c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": code, "message": message}})
			}
		}
		var str = func(name string) string {
			var v string
			_ = json.Unmarshal(m.Params[name], &v)
			return v
		}
		switch m.Method {
		case "initialize":
			s.mu.Lock()
			s.clients[str("clientId")] = true
			c.subs[ahp.RootChannel] = true
			s.mu.Unlock()
			answer(map[string]any{"protocolVersion": "1.0.0", "serverInfo": map[string]any{"name": "scripted-ahp", "version": "1"}, "snapshots": []any{}})
		case "reconnect":
			var subs []string
			var last int
			_ = json.Unmarshal(m.Params["subscriptions"], &subs)
			_ = json.Unmarshal(m.Params["lastSeenServerSeq"], &last)
			s.mu.Lock()
			known := s.clients[str("clientId")]
			s.clients[str("clientId")] = true
			for _, uri := range subs {
				c.subs[uri] = true
			}
			var result map[string]any
			if known {
				actions := []envelope{}
				for _, e := range s.log {
					if e.ServerSeq > last && c.subs[e.Channel] {
						actions = append(actions, e)
					}
				}
				result = map[string]any{"type": "replay", "actions": actions, "missing": []string{}}
			} else {
				snapshots := []any{}
				for _, uri := range subs {
					if snap := s.snapshotLocked(uri); snap != nil {
						snapshots = append(snapshots, snap)
					}
				}
				result = map[string]any{"type": "snapshot", "snapshots": snapshots}
			}
			s.mu.Unlock()
			answer(result)
		case "ping":
			answer(map[string]any{})
		case "listSessions":
			// every session of the server: the filter keeps the main one
			answer(map[string]any{"items": []any{
				map[string]any{"resource": s.main.Session, "title": s.title, "defaultChat": s.main.Chat},
				map[string]any{"resource": "ahp-session:/ses_other", "title": "a session opened with /new"},
			}})
		case "subscribe":
			channel := str("channel")
			s.mu.Lock()
			if s.refuse > 0 {
				s.refuse--
				s.mu.Unlock()
				fail(-32003, "cannot subscribe to "+channel)
				continue
			}
			c.subs[channel] = true
			snap := s.snapshotLocked(channel)
			s.mu.Unlock()
			if snap == nil {
				fail(-32003, "cannot subscribe to "+channel)
				continue
			}
			answer(map[string]any{"snapshot": snap})
		case "unsubscribe":
			s.mu.Lock()
			delete(c.subs, str("channel"))
			s.mu.Unlock()
			answer(map[string]any{})
		case "dispatchAction":
			var action map[string]any
			_ = json.Unmarshal(m.Params["action"], &action)
			var clientSeq int
			_ = json.Unmarshal(m.Params["clientSeq"], &clientSeq)
			s.dispatch(action, map[string]any{"clientId": "hub", "clientSeq": clientSeq})
		}
	}
}

// snapshotLocked is the state of a channel, as a subscribe answers it.
func (s *scriptedAHP) snapshotLocked(channel string) map[string]any {
	var state map[string]any
	switch channel {
	case s.main.Chat:
		state = map[string]any{"resource": s.main.Chat, "title": s.title, "status": 1, "modifiedAt": time.Now().UTC().Format(time.RFC3339), "turns": append([]map[string]any{}, s.turns...)}
		if s.live != nil {
			state["activeTurn"] = s.live
		}
		for _, p := range s.pending {
			if p["kind"] == "steering" {
				state["steeringMessage"] = p
			}
		}
		if len(s.pending) > 0 {
			state["queuedMessages"] = append([]map[string]any{}, s.pending...)
		}
	case s.main.Session:
		state = map[string]any{"resource": s.main.Session, "provider": "opencode", "title": s.title, "status": 1, "createdAt": "2026-11-25T10:00:00Z",
			"modifiedAt": time.Now().UTC().Format(time.RFC3339), "lifecycle": "ready", "activeClients": []any{}, "chats": []any{}, "defaultChat": s.main.Chat}
	case ahp.RootChannel:
		state = map[string]any{"agents": []any{}}
	default:
		return nil
	}
	return map[string]any{"resource": channel, "state": state, "fromSeq": s.seq}
}

// publish sends an action of a channel to every connection subscribed to it, as an envelope with the next serverSeq.
func (s *scriptedAHP) publish(channel string, action map[string]any, origin map[string]any, rejection string) {
	s.mu.Lock()
	s.seq++
	e := envelope{Channel: channel, Action: action, ServerSeq: s.seq, Origin: origin, RejectionReason: rejection}
	s.log = append(s.log, e)
	conns := make([]*ahpConn, 0, len(s.conns))
	for c := range s.conns {
		if c.subs[channel] {
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.send(map[string]any{"jsonrpc": "2.0", "method": "action", "params": e})
	}
}

func (s *scriptedAHP) chatAction(action map[string]any) { s.publish(s.main.Chat, action, nil, "") }

// rejectNext makes the next dispatched action be rejected, for why.
func (s *scriptedAHP) rejectNext(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = why
}

func (s *scriptedAHP) dispatch(action map[string]any, origin map[string]any) {
	s.mu.Lock()
	why := s.reject
	s.reject = ""
	s.mu.Unlock()
	if why != "" {
		s.publish(s.main.Chat, action, origin, why)
		return
	}
	switch action["type"] {
	case "chat/turnStarted":
		id, _ := action["turnId"].(string)
		message, _ := action["message"].(map[string]any)
		text, _ := message["text"].(string)
		s.mu.Lock()
		busy := s.live != nil
		s.mu.Unlock()
		if busy {
			s.publish(s.main.Chat, action, origin, "a turn is already active; follow-up messages go via chat/pendingMessageSet")
			return
		}
		s.start(id, text, action, origin)
	case "chat/turnCancelled":
		id, _ := action["turnId"].(string)
		s.finish(id, "cancelled", map[string]any{"type": "chat/turnCancelled", "turnId": id, "duration": 1})
	case "chat/pendingMessageSet":
		id, _ := action["id"].(string)
		kind, _ := action["kind"].(string)
		message, _ := action["message"].(map[string]any)
		text, _ := message["text"].(string)
		s.mu.Lock()
		busy := s.live != nil
		turn, _ := s.live["id"].(string)
		if busy && kind == "queued" {
			s.pending = append(s.pending, map[string]any{"id": id, "kind": kind, "message": message})
		}
		s.mu.Unlock()
		s.publish(s.main.Chat, action, origin, "")
		if !busy { // no turn runs: it starts one now
			s.start(fmt.Sprintf("turn-%d", time.Now().UnixNano()), text, map[string]any{"queuedMessageId": id}, nil)
		} else if kind == "steering" { // OpenCode takes it at the turn's next step, and the plugin says so then
			go func() {
				time.Sleep(400 * time.Millisecond)
				if s.running(turn) {
					s.chatAction(map[string]any{"type": "chat/pendingMessageRemoved", "kind": "steering", "id": id})
				}
			}()
		}
	case "chat/inputCompleted", "chat/toolCallConfirmed":
		key, _ := action["requestId"].(string)
		if action["type"] == "chat/toolCallConfirmed" {
			key, _ = action["toolCallId"].(string)
		}
		s.mu.Lock()
		answer := s.asked[key]
		delete(s.asked, key)
		s.mu.Unlock()
		if answer == nil {
			s.publish(s.main.Chat, action, origin, "nothing waits for that answer")
			return
		}
		s.publish(s.main.Chat, action, origin, "")
		answer <- action
	case "chat/pendingMessageRemoved":
		s.publish(s.main.Chat, action, origin, "cannot remove a pending message: opencode has no inbox cancel")
	}
}

// say is a prompt typed into the agent's terminal (a person, or a message egzo typed): the plugin mirrors its turn into the chat.
func (s *scriptedAHP) say(text string) {
	s.start(fmt.Sprintf("turn-tui-%d", time.Now().UnixNano()), text, nil, nil)
}

// start begins a turn. extra is what the action carries besides (a client's action as it sent it, or the queued message it starts from).
func (s *scriptedAHP) start(id, text string, extra map[string]any, origin map[string]any) {
	message := map[string]any{"text": text, "origin": map[string]any{"kind": "user"}}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	if s.live != nil {
		s.mu.Unlock()
		return // one turn at a time, as OpenCode
	}
	s.live = map[string]any{"id": id, "message": message, "responseParts": []any{}, "startedAt": startedAt}
	if q, ok := extra["queuedMessageId"].(string); ok {
		kept := s.pending[:0]
		for _, p := range s.pending {
			if p["id"] != q {
				kept = append(kept, p)
			}
		}
		s.pending = kept
	}
	s.mu.Unlock()
	action := map[string]any{"type": "chat/turnStarted", "turnId": id, "startedAt": startedAt, "message": message}
	for k, v := range extra {
		if _, set := action[k]; !set {
			action[k] = v
		}
	}
	s.publish(s.main.Chat, action, origin, "")
	go s.run(id, text)
}

func (s *scriptedAHP) running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live != nil && s.live["id"] == id
}

// part keeps what a turn's action adds to the running turn, for a snapshot taken while it runs.
func (s *scriptedAHP) part(id string, action map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil || s.live["id"] != id {
		return
	}
	parts, _ := s.live["responseParts"].([]any)
	switch action["type"] {
	case "chat/responsePart":
		s.live["responseParts"] = append(parts, action["part"])
	case "chat/delta", "chat/reasoning":
		for _, p := range parts {
			if part, ok := p.(map[string]any); ok && part["id"] == action["partId"] {
				part["content"] = fmt.Sprint(part["content"]) + fmt.Sprint(action["content"])
			}
		}
	case "chat/toolCallStart":
		s.live["responseParts"] = append(parts, map[string]any{"kind": "toolCall", "toolCall": map[string]any{
			"toolCallId": action["toolCallId"], "toolName": action["toolName"], "displayName": action["displayName"], "status": "streaming"}})
	case "chat/toolCallReady", "chat/toolCallComplete", "chat/toolCallContentChanged":
		for _, p := range parts {
			part, _ := p.(map[string]any)
			tc, _ := part["toolCall"].(map[string]any)
			if tc == nil || tc["toolCallId"] != action["toolCallId"] {
				continue
			}
			switch action["type"] {
			case "chat/toolCallReady":
				tc["status"], tc["invocationMessage"], tc["toolInput"], tc["confirmed"] = "running", action["invocationMessage"], action["toolInput"], "not-needed"
			case "chat/toolCallContentChanged":
				tc["content"] = action["content"]
			default:
				tc["status"] = "completed"
				for k, v := range action["result"].(map[string]any) {
					tc[k] = v
				}
			}
		}
	case "chat/usage":
		s.live["usage"] = action["usage"]
	case "chat/inputRequested":
		s.live["responseParts"] = append(parts, map[string]any{"kind": "inputRequest", "request": action["request"]})
	}
}

// ask waits for a client's answer to what the running turn id asks (a question, or a tool call's approval), under key; nil when the turn
// ended first.
func (s *scriptedAHP) ask(id, key string) map[string]any {
	answer := make(chan map[string]any, 1)
	s.mu.Lock()
	s.asked[key] = answer
	s.mu.Unlock()
	for s.running(id) {
		select {
		case a := <-answer:
			return a
		case <-time.After(100 * time.Millisecond):
		}
	}
	s.mu.Lock()
	delete(s.asked, key)
	s.mu.Unlock()
	return nil
}

// run plays a turn the way OpenCode's events become actions in the plugin.
func (s *scriptedAHP) run(id, text string) {
	step := func(action map[string]any) bool {
		time.Sleep(150 * time.Millisecond)
		if !s.running(id) {
			return false
		}
		action["turnId"] = id
		s.part(id, action)
		s.chatAction(action)
		return true
	}
	if !step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "reasoning", "id": "r1", "content": "Looking at the "}}) ||
		!step(map[string]any{"type": "chat/reasoning", "partId": "r1", "content": "repository first."}) {
		return
	}
	if strings.Contains(text, "search") { // the web-search consent OpenCode asks before its first search
		if !step(map[string]any{"type": "chat/toolCallStart", "toolCallId": "call-ws", "toolName": "websearch", "displayName": "Web search"}) ||
			!step(map[string]any{"type": "chat/toolCallReady", "toolCallId": "call-ws", "invocationMessage": "egzo agent sandbox", "toolInput": `{"query":"egzo agent sandbox"}`, "confirmed": "not-needed"}) ||
			!step(map[string]any{"type": "chat/inputRequested", "request": map[string]any{"id": "frm-ws", "message": "Web Search", "questions": []any{map[string]any{
				"id": "choice", "kind": "single-select", "message": "Allow OpenCode to search the web for up-to-date information?", "required": true,
				"options": []any{
					map[string]any{"id": "allow", "label": "Allow search via Exa"},
					map[string]any{"id": "choose", "label": "Choose another provider"},
					map[string]any{"id": "disable", "label": "Disable web search"},
				}}}}}) {
			return
		}
		answer := s.ask(id, "frm-ws")
		if answer == nil {
			return
		}
		choice := ""
		if answers, ok := answer["answers"].(map[string]any); ok {
			if a, ok := answers["choice"].(map[string]any); ok {
				if v, ok := a["value"].(map[string]any); ok {
					choice, _ = v["value"].(string)
				}
			}
		}
		said := "The search was declined."
		if answer["response"] == "accept" {
			said = "Searched the web (" + choice + "): egzo is an agent sandbox."
		}
		if !step(map[string]any{"type": "chat/toolCallComplete", "toolCallId": "call-ws", "result": map[string]any{"success": answer["response"] == "accept", "pastTenseMessage": "Searched the web"}}) ||
			!step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "markdown", "id": "m-ws", "content": said}}) {
			return
		}
		time.Sleep(150 * time.Millisecond)
		s.finish(id, "complete", map[string]any{"type": "chat/turnComplete", "turnId": id, "duration": 900})
		return
	}
	if strings.Contains(text, "approve") { // a tool call that waits for the person's approval
		if !step(map[string]any{"type": "chat/toolCallStart", "toolCallId": "call-rm", "toolName": "bash", "displayName": "Shell"}) ||
			!step(map[string]any{"type": "chat/toolCallReady", "toolCallId": "call-rm", "invocationMessage": "Run `rm -rf build`", "toolInput": `{"command":"rm -rf build"}`, "confirmationTitle": "Delete the build directory?"}) {
			return
		}
		answer := s.ask(id, "call-rm")
		if answer == nil {
			return
		}
		said := "It was denied: the build directory stays."
		if answer["approved"] == true {
			said = "Deleted the build directory."
			if !step(map[string]any{"type": "chat/toolCallComplete", "toolCallId": "call-rm", "result": map[string]any{"success": true, "pastTenseMessage": "Ran `rm -rf build`"}}) {
				return
			}
		}
		if !step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "markdown", "id": "m-rm", "content": said}}) {
			return
		}
		time.Sleep(150 * time.Millisecond)
		s.finish(id, "complete", map[string]any{"type": "chat/turnComplete", "turnId": id, "duration": 900})
		return
	}
	if strings.Contains(text, "test") {
		if !step(map[string]any{"type": "chat/toolCallStart", "toolCallId": "call-sh", "toolName": "bash", "displayName": "Shell"}) ||
			!step(map[string]any{"type": "chat/toolCallReady", "toolCallId": "call-sh", "invocationMessage": "Run `go test ./...`", "toolInput": `{"command":"go test ./...","description":"Run the tests"}`, "confirmed": "not-needed"}) ||
			!step(map[string]any{"type": "chat/toolCallContentChanged", "toolCallId": "call-sh", "content": []any{map[string]any{"type": "text", "text": "ok  \tegzo/parser\t0.412s"}}}) ||
			!step(map[string]any{"type": "chat/toolCallComplete", "toolCallId": "call-sh", "result": map[string]any{"success": false, "pastTenseMessage": "Ran `go test ./...`",
				"content": []any{map[string]any{"type": "text", "text": "ok  \tegzo/parser\t0.412s\nFAIL\tegzo/lexer\t0.031s"}}, "error": map[string]any{"message": "exit status 1"}}}) {
			return
		}
	}
	if !step(map[string]any{"type": "chat/toolCallStart", "toolCallId": "call-1", "toolName": "read", "displayName": "Read file"}) ||
		!step(map[string]any{"type": "chat/toolCallReady", "toolCallId": "call-1", "invocationMessage": "README.md", "toolInput": `{"filePath":"README.md"}`, "confirmed": "not-needed"}) {
		return
	}
	if strings.Contains(text, "wait") {
		return // runs until someone stops it, or a message is queued for after it (then it ends after a moment)
	}
	if strings.Contains(text, "fail") {
		s.finish(id, "error", map[string]any{"type": "chat/error", "turnId": id, "duration": 1,
			"part": map[string]any{"kind": "error", "error": map[string]any{"errorType": "APIError", "message": "the model refused the request"}}})
		return
	}
	if !step(map[string]any{"type": "chat/toolCallComplete", "toolCallId": "call-1", "result": map[string]any{"success": true, "pastTenseMessage": "Read README.md",
		"content": []any{map[string]any{"type": "text", "text": "# egzo\n\nBuild with `make`."}}}}) ||
		!step(map[string]any{"type": "chat/responsePart", "part": map[string]any{"kind": "markdown", "id": "m1", "content": "The README says how to "}}) ||
		!step(map[string]any{"type": "chat/delta", "partId": "m1", "content": "**build** the project:\n\n"}) ||
		!step(map[string]any{"type": "chat/delta", "partId": "m1", "content": "```sh\nmake\n```\n"}) ||
		!step(map[string]any{"type": "chat/usage", "usage": map[string]any{"inputTokens": 12400, "outputTokens": 310, "cacheReadTokens": 9000, "model": "opencode-go/glm-5.3-flash"}}) {
		return
	}
	time.Sleep(150 * time.Millisecond)
	s.finish(id, "complete", map[string]any{"type": "chat/turnComplete", "turnId": id, "duration": 2100})
}

// finish ends the running turn id: it joins the history, and its last action goes out. The steering message it took goes, the session gets
// a title after its first turn of the page, and a queued message starts the next turn.
func (s *scriptedAHP) finish(id, state string, last map[string]any) {
	s.mu.Lock()
	if s.live == nil || s.live["id"] != id {
		s.mu.Unlock()
		return
	}
	s.live["state"] = state
	s.live["duration"] = last["duration"]
	s.turns = append(s.turns, s.live)
	s.live = nil
	var steering map[string]any
	var queued map[string]any
	kept := []map[string]any{}
	for _, p := range s.pending {
		switch {
		case p["kind"] == "steering" && steering == nil:
			steering = p
		case p["kind"] == "queued" && queued == nil:
			queued = p
			kept = append(kept, p)
		default:
			kept = append(kept, p)
		}
	}
	s.pending = kept
	retitle := s.title == "Explain the parser" && state == "complete"
	if retitle {
		s.title = "Build the project"
	}
	s.mu.Unlock()
	s.chatAction(last)
	if steering != nil {
		s.chatAction(map[string]any{"type": "chat/pendingMessageRemoved", "kind": "steering", "id": steering["id"]})
	}
	if retitle {
		s.publish(s.main.Session, map[string]any{"type": "session/titleChanged", "title": "Build the project"}, nil, "")
	}
	if queued != nil {
		message, _ := queued["message"].(map[string]any)
		text, _ := message["text"].(string)
		s.start(fmt.Sprintf("turn-q-%d", time.Now().UnixNano()), text, map[string]any{"queuedMessageId": queued["id"]}, nil)
	}
}

// drop closes every connection, as a network that went away: the page must come back on its own.
func (s *scriptedAHP) drop() {
	s.mu.Lock()
	conns := make([]*ahpConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.out.Close()
	}
}

// heardLines is what clients sent the server, after the filter.
func (s *scriptedAHP) heardLines() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage{}, s.heard...)
}
