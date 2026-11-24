// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"encoding/json"
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
	// lists are the ids of listSessions the client asked: their answers are cut down to the main session.
	lists map[string]bool
}

// The chat actions a client may dispatch: start a turn (send a prompt), cancel one, and set or clear a pending message (steering, queueing).
var clientActions = map[string]bool{
	"chat/turnStarted":           true,
	"chat/turnCancelled":         true,
	"chat/pendingMessageSet":     true,
	"chat/pendingMessageRemoved": true,
}

// The commands a client may send, whatever their params; the rest are checked one by one in FromClient, or refused.
var freeCommands = map[string]bool{"ping": true, "reconnect": true}

type message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// errNotAllowed is the JSON-RPC error a refused command gets: method not found, as far as the client can tell.
const errNotAllowed = -32601

// FromClient decides about a line from the hub's client. It returns the line to send on to the server, or, for a refused request, nil and
// the error to answer the client with (nil for a refused notification, which gets nothing).
func (f *Filter) FromClient(line []byte) (forward []byte, reply []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil || m.Method == "" {
		return nil, nil // not a request: a client has nothing else to say
	}
	refuse := func(why string) ([]byte, []byte) {
		if len(m.ID) == 0 || string(m.ID) == "null" {
			return nil, nil
		}
		answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": errNotAllowed, "message": "egzo: " + why}})
		return nil, answer
	}
	var params map[string]json.RawMessage
	_ = json.Unmarshal(m.Params, &params)
	channel := stringOf(params["channel"])
	switch {
	case freeCommands[m.Method]:
		return line, nil
	case m.Method == "initialize":
		if channel != RootChannel {
			return refuse("initialize targets " + RootChannel)
		}
		var initial []string
		if raw, ok := params["initialSubscriptions"]; ok && json.Unmarshal(raw, &initial) != nil {
			return refuse("initialSubscriptions is a list of channels")
		}
		for _, uri := range initial {
			if !f.readable(uri) {
				return refuse("only the agent's main session can be subscribed to")
			}
		}
		return line, nil
	case m.Method == "subscribe" || m.Method == "unsubscribe":
		if !f.readable(channel) {
			return refuse("only the agent's main session can be subscribed to")
		}
		return line, nil
	case m.Method == "listSessions":
		if len(m.ID) > 0 {
			f.mu.Lock()
			if f.lists == nil {
				f.lists = map[string]bool{}
			}
			f.lists[string(m.ID)] = true
			f.mu.Unlock()
		}
		return line, nil
	case m.Method == "fetchTurns":
		if !f.isMainChat(channel) {
			return refuse("only the main session's chat")
		}
		return line, nil
	case m.Method == "dispatchAction":
		if f.ReadOnly {
			return refuse("read-only: this client watches the chat")
		}
		var action struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(params["action"], &action)
		if !f.isMainChat(channel) || !clientActions[action.Type] {
			return refuse("only prompts, cancels and pending messages in the main session's chat")
		}
		return line, nil
	}
	return refuse(m.Method + " is not available through egzo")
}

// FromServer decides about a line from the agent's AHP server, or from the bridge: what the client may see. It returns the line to send
// on, possibly cut down, or nil. Notes of the main session update the filter.
func (f *Filter) FromServer(line []byte) []byte {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	if m.Method == "" {
		// an answer: a listSessions the client asked is cut down to the main session
		f.mu.Lock()
		list := f.lists[string(m.ID)]
		delete(f.lists, string(m.ID))
		f.mu.Unlock()
		if list && len(m.Result) > 0 {
			return f.cutList(m)
		}
		return line
	}
	var params map[string]json.RawMessage
	_ = json.Unmarshal(m.Params, &params)
	switch m.Method {
	case MainSessionMethod:
		var main MainSession
		if json.Unmarshal(m.Params, &main) != nil || main.Session == "" || main.Chat == "" {
			return nil
		}
		f.mu.Lock()
		f.main = main
		f.mu.Unlock()
		return line
	case "root/sessionAdded":
		var added struct {
			Summary struct {
				Resource string `json:"resource"`
			} `json:"summary"`
		}
		_ = json.Unmarshal(m.Params, &added)
		if !f.isMainSession(added.Summary.Resource) {
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
	if json.Unmarshal(m.Result, &result) != nil {
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
	m.Result, _ = json.Marshal(result)
	m.JSONRPC = "2.0"
	out, _ := json.Marshal(m)
	return out
}

func stringOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
