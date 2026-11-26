// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sync"
)

// RootChannel is AHP's root channel: the host's catalogue, which every client subscribes to on initialize.
const RootChannel = "ahp-root://"

// Filter decides what passes between a hub's client and an agent's AHP server: the chat of the agent's main session, and nothing else. A
// hub cannot read a file (resource*), open a terminal, touch a changeset, or create, dispose or reach any other session. ReadOnly also
// keeps the client from acting in the chat: it watches. One Filter serves one connection.
type Filter struct {
	ReadOnly bool

	mu   sync.Mutex
	main MainSession
	// pending are the requests the client sent on that the server has not answered, by their id (idKey): the method of each. The answer
	// to a listSessions is cut down to the main session, and two requests cannot share an id, or one's answer would take the other's cut.
	pending map[string]string
}

// The chat actions a client may dispatch: start a turn (send a prompt), cancel one, and set or clear a pending message (steering, queueing).
var clientActions = map[string]bool{
	"chat/turnStarted":           true,
	"chat/turnCancelled":         true,
	"chat/pendingMessageSet":     true,
	"chat/pendingMessageRemoved": true,
}

// The commands a client may send, whatever their params; the rest are checked one by one in FromClient, or refused.
var freeCommands = map[string]bool{"ping": true}

// message is a JSON-RPC message read as the agent's AHP server reads it, with JSON.parse: names are exact, and of two equal names the last
// counts. Go's decoder into a struct would match "Method" or "METHOD" too, so a line could say one method to the filter and another to the
// server. Each object the filter looks into is read into a map, which keeps names as they are and the last of two equal ones.
type message struct {
	ID     json.RawMessage
	Method string
	Params map[string]json.RawMessage
	Result json.RawMessage
	fields map[string]json.RawMessage
}

func parse(line []byte) (message, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil || fields == nil {
		return message{}, false
	}
	m := message{ID: fields["id"], Method: stringOf(fields["method"]), Result: fields["result"], fields: fields}
	_ = json.Unmarshal(fields["params"], &m.Params)
	return m, true
}

// field reads one name of an object exactly, as JSON.parse would.
func field(raw json.RawMessage, name string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields[name]
}

// canonicalInteger is how JSON.stringify writes a safe integer: what a number id must look like to come back from the server as it went.
var canonicalInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})$`)

// idKey is a request's id as the server will echo it: a string by its value (escapes do not count), an integer as JSON.stringify writes
// it. ok is false for an id the filter cannot match its answer to (1.0, 1e3, an object), which it refuses; a notification has the key "".
func idKey(raw json.RawMessage) (key string, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "s:" + s, true
	}
	if canonicalInteger.Match(raw) {
		return "n:" + string(raw), true
	}
	return "", false
}

// errNotAllowed is the JSON-RPC error a refused command gets: method not found, as far as the client can tell.
const errNotAllowed = -32601

// FromClient decides about a line from the hub's client. It returns the line to send on to the server, or, for a refused request, nil and
// the error to answer the client with (nil for a refused notification, which gets nothing).
func (f *Filter) FromClient(line []byte) (forward []byte, reply []byte) {
	m, ok := parse(line)
	if !ok || m.Method == "" {
		return nil, nil // not a request: a client has nothing else to say
	}
	refuse := func(why string) ([]byte, []byte) {
		if len(m.ID) == 0 || string(m.ID) == "null" {
			return nil, nil
		}
		answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": errNotAllowed, "message": "egzo: " + why}})
		return nil, answer
	}
	key, ok := idKey(m.ID)
	if !ok {
		return refuse("an id is a string or an integer")
	}
	if why := f.allowed(m); why != "" {
		return refuse(why)
	}
	if key != "" {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, taken := f.pending[key]; taken {
			return refuse("that id is in use by a request not answered yet")
		}
		if f.pending == nil {
			f.pending = map[string]string{}
		}
		f.pending[key] = m.Method
	}
	return line, nil
}

// allowed says why the client may not send a request, or "" when it may.
func (f *Filter) allowed(m message) string {
	channel := stringOf(m.Params["channel"])
	switch {
	case freeCommands[m.Method]:
		return ""
	case m.Method == "initialize":
		if channel != RootChannel {
			return "initialize targets " + RootChannel
		}
		var initial []string
		if raw, ok := m.Params["initialSubscriptions"]; ok && json.Unmarshal(raw, &initial) != nil {
			return "initialSubscriptions is a list of channels"
		}
		for _, uri := range initial {
			if !f.readable(uri) {
				return "only the agent's main session can be subscribed to"
			}
		}
		return ""
	case m.Method == "reconnect":
		// the server subscribes the connection to every channel it names, and its answer holds their snapshots or the actions it missed
		if channel != RootChannel {
			return "reconnect targets " + RootChannel
		}
		var subscriptions []string
		if json.Unmarshal(m.Params["subscriptions"], &subscriptions) != nil {
			return "subscriptions is a list of channels"
		}
		for _, uri := range subscriptions {
			if !f.readable(uri) {
				return "only the agent's main session can be subscribed to"
			}
		}
		return ""
	case m.Method == "subscribe" || m.Method == "unsubscribe":
		if !f.readable(channel) {
			return "only the agent's main session can be subscribed to"
		}
		return ""
	case m.Method == "listSessions":
		return ""
	case m.Method == "fetchTurns":
		if !f.isMainChat(channel) {
			return "only the main session's chat"
		}
		return ""
	case m.Method == "dispatchAction":
		if f.ReadOnly {
			return "read-only: this client watches the chat"
		}
		if !f.isMainChat(channel) || !clientActions[stringOf(field(m.Params["action"], "type"))] {
			return "only prompts, cancels and pending messages in the main session's chat"
		}
		return ""
	}
	return m.Method + " is not available through egzo"
}

// FromServer decides about a line from the agent's AHP server, or from the bridge: what the client may see. It returns the line to send
// on, possibly cut down, or nil. Notes of the main session update the filter.
func (f *Filter) FromServer(line []byte) []byte {
	m, ok := parse(line)
	if !ok {
		return nil
	}
	if m.Method == "" {
		// an answer: a listSessions the client asked is cut down to the main session
		key, _ := idKey(m.ID)
		f.mu.Lock()
		method := f.pending[key]
		delete(f.pending, key)
		f.mu.Unlock()
		if method == "listSessions" && len(m.Result) > 0 {
			return f.cutList(m)
		}
		return line
	}
	params := m.Params
	switch m.Method {
	case MainSessionMethod:
		main := MainSession{Session: stringOf(params["session"]), Chat: stringOf(params["chat"])}
		if main.Session == "" || main.Chat == "" {
			return nil
		}
		f.mu.Lock()
		f.main = main
		f.mu.Unlock()
		return line
	case "root/sessionAdded":
		if !f.isMainSession(stringOf(field(params["summary"], "resource"))) {
			return nil
		}
		return line
	case "root/sessionRemoved", "root/sessionSummaryChanged":
		if !f.isMainSession(stringOf(params["session"])) {
			return nil
		}
		return line
	}
	if channel := stringOf(params["channel"]); channel != "" && !f.readable(channel) {
		return nil
	}
	return line
}

// Main is the main session the filter knows, empty until the bridge said.
func (f *Filter) Main() MainSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.main
}

func (f *Filter) readable(channel string) bool {
	return channel == RootChannel || f.isMainSession(channel) || f.isMainChat(channel)
}

func (f *Filter) isMainSession(uri string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return uri != "" && uri == f.main.Session
}

func (f *Filter) isMainChat(uri string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return uri != "" && uri == f.main.Chat
}

func (f *Filter) cutList(m message) []byte {
	var result map[string]json.RawMessage
	if json.Unmarshal(m.Result, &result) != nil || result == nil {
		return nil
	}
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(result["items"], &items)
	kept := []map[string]json.RawMessage{}
	for _, item := range items {
		if f.isMainSession(stringOf(item["resource"])) {
			kept = append(kept, item)
		}
	}
	result["items"], _ = json.Marshal(kept)
	delete(result, "nextCursor")
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	return out
}

func stringOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
