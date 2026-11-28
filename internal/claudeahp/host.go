// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package claudeahp is Claude Code's AHP adapter: an AHP server (https://github.com/microsoft/agent-host-protocol) that runs next to
// Claude Code's TUI, inside the agent's container, and serves the chat of the agent's main session, the way egzo's fork of opencode-ahp
// does inside OpenCode. On the wire it is the same: `egzo agent ahp`, the link's filter and the hub's Rich UI do not know which one they
// talk to.
//
// Claude Code has no server of its own to ask, so the adapter puts the chat together from what Claude Code leaves behind:
//
//   - the session's transcript (transcript.go), which it follows: the prompts, the answers, the tool calls and their results, steering,
//     interrupts and the end of each turn. It is the source of the chat; after a restart, reading it again gives the same chat back.
//   - Claude Code's hooks, which egzo's settings point at the adapter (`egzo agent claude-ahp hook`): PermissionRequest makes a tool call
//     wait for an approval, or an AskUserQuestion wait for its answers, in the chat and in the TUI at once, and the first answer wins; Stop
//     and StopFailure end a turn the transcript did not; SessionStart says the TUI (re)started.
//   - the TUI itself, for what a client asks: a prompt or a steering message is typed into it, as a person would, and Esc stops a turn.
//     What is typed reaches the chat the same way as what a person types in the terminal: through the transcript.
package claudeahp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/internal/ahp"
)

// Typer is the way to Claude Code's TUI: typing a line as a person would, and pressing Esc.
type Typer interface {
	// Prompt types text and Enter. idle says no turn runs: the TUI is waited for until it shows it takes a prompt (a turn that runs takes
	// what is typed as steering, whatever its screen shows).
	Prompt(text string, idle bool) error
	// Interrupt presses the TUI's interrupt key.
	Interrupt() error
}

// Config is what a Host serves.
type Config struct {
	// Session is Claude Code's id of the agent's main session: the one the TUI was started on (--session-id, --resume).
	Session string
	// Typer reaches the TUI; nil turns down what needs it.
	Typer Typer
	// Version is the adapter's, for initialize.
	Version string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Host is the AHP server of one main session: its chat, its connections, and the actions it sent, for clients that reconnect.
type Host struct {
	cfg  Config
	main ahp.MainSession

	mu      sync.Mutex
	title   string
	created string
	turns   []map[string]any // finished, as a snapshot holds them
	live    *turn
	// steering is the message for the turn that runs that Claude Code has not taken yet: the hub's, or one typed in the terminal
	steering *pendingMessage
	// queued are the hub's messages for after the turn that runs, kept here and typed when it ends
	queued []pendingMessage
	seq    int
	log    []envelope
	conns  map[*conn]struct{}
	// clients are the clientIds that initialized or reconnected: one known gets what it missed, an unknown one a snapshot
	clients map[string]bool
	// waits are the answers a hook waits for, by tool call id: an approval (chat/toolCallConfirmed) or answers (chat/inputCompleted)
	waits map[string]*waiter
	// changed is closed, and made anew, whenever the transcript adds to the chat: a hook that waits for its tool call to be read wakes up
	changed chan struct{}
	// typing carries what is to be typed into the TUI, in order
	typing chan func()
	// drain reads what the transcript has that was not read yet, and onTranscript follows the transcript a hook names; set by Run
	drain        func()
	onTranscript func(path string)
}

// waiter is a hook that waits for a client's answer: it gets the answer, or gone closes when there is nothing to answer any more (the tool
// call has its result, the turn ended).
type waiter struct {
	answer chan map[string]json.RawMessage
	gone   chan struct{}
}

func newWaiter() *waiter {
	return &waiter{answer: make(chan map[string]json.RawMessage, 1), gone: make(chan struct{})}
}

// dropWaitLocked forgets the wait of a tool call, and lets its hook go.
func (h *Host) dropWaitLocked(id string) {
	if w := h.waits[id]; w != nil {
		delete(h.waits, id)
		close(w.gone)
	}
}

// pendingMessage is a steering or queued message: AHP's PendingMessage.
type pendingMessage struct {
	ID      string
	Text    string
	FromHub bool
}

// envelope is AHP's ActionEnvelope.
type envelope struct {
	Channel         string         `json:"channel"`
	Action          map[string]any `json:"action"`
	ServerSeq       int            `json:"serverSeq"`
	Origin          map[string]any `json:"origin,omitempty"`
	RejectionReason string         `json:"rejectionReason,omitempty"`
}

// keptEnvelopes is how many actions a Host keeps for replays; a client that missed more gets snapshots.
const keptEnvelopes = 4096

// turn is the turn that runs.
type turn struct {
	id        string
	text      string
	startedAt string
	started   time.Time
	parts     []map[string]any
	usage     map[string]any
	// bound says the transcript has the prompt that started it: a turn a client started is live from the moment it is typed
	bound bool
	// tools are its tool calls, by id: the toolCall object inside its part
	tools map[string]map[string]any
	// past is what each tool call did, in words, for when it completes
	past map[string]string
	// asked are the tool calls a hook asked about, and asks the questions (AskUserQuestion) by tool call id
	asked map[string]bool
	asks  map[string]askInput
	// counted are the API messages whose usage is in usage
	counted map[string]bool
	output  int64
	// failure is the API error Claude Code reported, if the turn ended on one
	failure string
}

func (t *turn) json(state string, duration int64) map[string]any {
	parts := make([]any, 0, len(t.parts))
	for _, p := range t.parts {
		parts = append(parts, p)
	}
	out := map[string]any{"id": t.id, "message": userMessage(t.text), "startedAt": t.startedAt, "responseParts": parts}
	if t.usage != nil {
		out["usage"] = t.usage
	}
	if state != "" {
		out["state"] = state
		out["duration"] = duration
	}
	return out
}

func userMessage(text string) map[string]any {
	return map[string]any{"text": text, "origin": map[string]any{"kind": "user"}}
}

// New makes the Host of a main session.
func New(cfg Config) *Host {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &Host{
		cfg:     cfg,
		main:    ahp.SessionURIs(cfg.Session),
		created: cfg.Now().UTC().Format(time.RFC3339),
		conns:   map[*conn]struct{}{},
		clients: map[string]bool{},
		waits:   map[string]*waiter{},
		changed: make(chan struct{}),
		typing:  make(chan func(), 1024),
		drain:   func() {},
	}
	go func() {
		for job := range h.typing {
			job()
		}
	}()
	return h
}

// Main is the main session's URIs.
func (h *Host) Main() ahp.MainSession { return h.main }

func (h *Host) now() string { return h.cfg.Now().UTC().Format(time.RFC3339Nano) }

// publish sends an action of a channel to every connection subscribed to it, as an envelope with the next serverSeq. h.mu is held.
func (h *Host) publish(channel string, action map[string]any, origin map[string]any, rejection string) {
	h.seq++
	e := envelope{Channel: channel, Action: action, ServerSeq: h.seq, Origin: origin, RejectionReason: rejection}
	h.log = append(h.log, e)
	if len(h.log) > keptEnvelopes {
		h.log = append([]envelope{}, h.log[len(h.log)-keptEnvelopes/2:]...)
	}
	note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "action", "params": e})
	for c := range h.conns {
		if c.subscribed(channel) {
			c.send(note)
		}
	}
}

// chat publishes an action of the main chat. h.mu is held.
func (h *Host) chat(action map[string]any) { h.publish(h.main.Chat, action, nil, "") }

// touched wakes up what waits for the chat to change. h.mu is held.
func (h *Host) touched() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// ─── Snapshots ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

// AHP's SessionStatus: idle, in progress, waiting for the person.
const (
	statusIdle        = 1
	statusInProgress  = 1 << 3
	statusInputNeeded = 1<<3 | 1<<4
)

func (h *Host) statusLocked() int {
	if h.live == nil {
		return statusIdle
	}
	for _, p := range h.live.parts {
		if p["kind"] == "inputRequest" && p["response"] == nil {
			return statusInputNeeded
		}
		if tc, ok := p["toolCall"].(map[string]any); ok && tc["status"] == "pending-confirmation" {
			return statusInputNeeded
		}
	}
	return statusInProgress
}

func (h *Host) titleLocked() string {
	if h.title != "" {
		return h.title
	}
	return "Claude Code"
}

// snapshotLocked is the state of a channel, as a subscribe answers it; nil for a channel the Host does not serve.
func (h *Host) snapshotLocked(channel string) map[string]any {
	var state map[string]any
	switch channel {
	case h.main.Chat:
		turns := make([]any, 0, len(h.turns))
		for _, t := range h.turns {
			turns = append(turns, t)
		}
		state = map[string]any{"resource": h.main.Chat, "title": h.titleLocked(), "status": h.statusLocked(), "modifiedAt": h.now(), "turns": turns}
		if h.live != nil {
			state["activeTurn"] = h.live.json("", 0)
		}
		if h.steering != nil {
			state["steeringMessage"] = map[string]any{"id": h.steering.ID, "message": userMessage(h.steering.Text)}
		}
		if len(h.queued) > 0 {
			queued := []any{}
			for _, q := range h.queued {
				queued = append(queued, map[string]any{"id": q.ID, "message": userMessage(q.Text)})
			}
			state["queuedMessages"] = queued
		}
	case h.main.Session:
		state = map[string]any{"resource": h.main.Session, "provider": "claude-code", "title": h.titleLocked(), "status": h.statusLocked(),
			"createdAt": h.created, "modifiedAt": h.now(), "lifecycle": "ready", "activeClients": []any{}, "chats": []any{},
			"defaultChat": h.main.Chat}
	case ahp.RootChannel:
		state = map[string]any{"agents": []any{map[string]any{"provider": "claude-code", "displayName": "Claude Code", "description": "Claude Code, through egzo"}}}
	default:
		return nil
	}
	return map[string]any{"resource": channel, "state": state, "fromSeq": h.seq}
}

func (h *Host) summaryLocked() map[string]any {
	return map[string]any{"resource": h.main.Session, "provider": "claude-code", "title": h.titleLocked(), "status": h.statusLocked(),
		"createdAt": h.created, "modifiedAt": h.now(), "defaultChat": h.main.Chat}
}

// ─── The transcript ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

// Apply takes one line of the main session's transcript into the chat.
func (h *Host) Apply(line []byte) {
	e, ok := parseEntry(line)
	if !ok || e.IsSidechain {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.touched()
	switch e.Type {
	case "user":
		kind, text, results := classifyUser(e)
		switch kind {
		case userPrompt:
			h.promptedLocked(text, e)
		case userResults:
			for _, b := range results {
				h.resultLocked(b, e.ToolUseResult)
			}
		case userInterrupt:
			h.endLocked("cancelled", 0, e.Timestamp)
		}
	case "assistant":
		h.assistantLocked(e)
	case "queue-operation":
		h.queueLocked(e)
	case "system":
		// the end of the turn whose prompt the transcript has: not of a turn a client started that Claude Code has not taken yet, nor of
		// one that started after it (Claude Code writes it once the Stop hooks are done, and a prompt can be typed meanwhile)
		if e.Subtype == "turn_duration" && h.live != nil && h.live.bound {
			if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil || !at.Before(h.live.started) {
				h.endLocked("complete", e.DurationMs, e.Timestamp)
			}
		}
	case "ai-title":
		if e.AITitle != "" {
			h.setTitleLocked(e.AITitle)
		}
	case "custom-title":
		if e.CustomTitle != "" {
			h.setTitleLocked(e.CustomTitle)
		}
	}
}

func (h *Host) setTitleLocked(title string) {
	title = clip(oneLine(title), 120)
	if title == "" || title == h.title {
		return
	}
	h.title = title
	h.publish(h.main.Session, map[string]any{"type": "session/titleChanged", "title": title}, nil, "")
}

func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// promptedLocked is a prompt in the transcript: the turn a client started and typed, or a new one (typed in the terminal, by egzo, or a
// steering message the turn before did not take).
func (h *Host) promptedLocked(text string, e entry) {
	if h.live != nil && !h.live.bound {
		h.live.bound = true
		if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			h.live.started = at
		}
		return
	}
	if h.live != nil {
		// the end of the turn before was missed: it is over, since Claude Code took a new prompt
		h.endLocked("complete", 0, e.Timestamp)
	}
	id := "turn-" + e.UUID
	if e.UUID == "" {
		id = newID()
	}
	action := map[string]any{"type": "chat/turnStarted", "turnId": id, "message": userMessage(text)}
	if h.steering != nil && sameText(h.steering.Text, text) {
		action["queuedMessageId"] = h.steering.ID
		h.steering = nil
	}
	h.startLocked(id, text, e.Timestamp, action, nil)
	h.live.bound = true
}

// startLocked makes id the turn that runs, and says so with action (chat/turnStarted, as a client sent it or as the Host made it).
func (h *Host) startLocked(id, text, at string, action map[string]any, origin map[string]any) {
	started := h.cfg.Now()
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
		started = t
	} else {
		at = h.now()
	}
	h.live = &turn{id: id, text: text, startedAt: at, started: started, tools: map[string]map[string]any{}, past: map[string]string{},
		asked: map[string]bool{}, asks: map[string]askInput{}, counted: map[string]bool{}}
	action["startedAt"] = at
	h.publish(h.main.Chat, action, origin, "")
	if h.title == "" {
		h.setTitleLocked(text)
	}
}

func (h *Host) assistantLocked(e entry) {
	t := h.live
	if t == nil || e.Message == nil {
		return
	}
	if u := e.Message.Usage; u != nil && e.Message.ID != "" && !t.counted[e.Message.ID] {
		t.counted[e.Message.ID] = true
		t.output += u.OutputTokens
		t.usage = map[string]any{"inputTokens": u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens, "outputTokens": t.output,
			"cacheReadTokens": u.CacheReadInputTokens, "model": e.Message.Model}
		h.chat(map[string]any{"type": "chat/usage", "turnId": t.id, "usage": t.usage})
	}
	for i, b := range e.Message.blocks() {
		partID := e.UUID + "-" + string(rune('a'+i%26))
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			if e.IsAPIErrorMessage {
				t.failure = b.Text
				continue
			}
			h.partLocked(map[string]any{"kind": "markdown", "id": partID, "content": b.Text})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue // Claude Code keeps only the signature of thinking it does not show
			}
			h.partLocked(map[string]any{"kind": "reasoning", "id": partID, "content": b.Thinking})
		case "tool_use":
			if b.ID == "" || t.tools[b.ID] != nil {
				continue
			}
			words := describeTool(b.Name, b.Input)
			input := string(b.Input)
			if input == "" {
				input = "{}"
			}
			tc := map[string]any{"toolCallId": b.ID, "toolName": b.Name, "displayName": words.display, "status": "running",
				"invocationMessage": words.invocation, "toolInput": input, "confirmed": "not-needed"}
			t.tools[b.ID] = tc
			t.past[b.ID] = words.past
			t.parts = append(t.parts, map[string]any{"kind": "toolCall", "toolCall": tc})
			h.chat(map[string]any{"type": "chat/toolCallStart", "turnId": t.id, "toolCallId": b.ID, "toolName": b.Name, "displayName": words.display})
			h.chat(map[string]any{"type": "chat/toolCallReady", "turnId": t.id, "toolCallId": b.ID, "invocationMessage": words.invocation,
				"toolInput": input, "confirmed": "not-needed"})
		}
	}
}

func (h *Host) partLocked(part map[string]any) {
	h.live.parts = append(h.live.parts, part)
	h.chat(map[string]any{"type": "chat/responsePart", "turnId": h.live.id, "part": part})
}

func (h *Host) resultLocked(b block, record json.RawMessage) {
	t := h.live
	if t == nil {
		return
	}
	tc := t.tools[b.ToolUseID]
	if tc == nil {
		return
	}
	if ask, ok := t.asks[b.ToolUseID]; ok {
		h.answeredLocked(b.ToolUseID, ask, record, b.IsError)
	}
	h.dropWaitLocked(b.ToolUseID)
	status := tc["status"]
	if status != "running" && status != "pending-confirmation" {
		return
	}
	text := clipResult(resultText(b.Content))
	result := map[string]any{"success": !b.IsError, "pastTenseMessage": t.past[b.ToolUseID]}
	if text != "" {
		result["content"] = []any{map[string]any{"type": "text", "text": text}}
	}
	if b.IsError {
		result["error"] = map[string]any{"message": clip(oneLine(text), 300)}
	}
	if status == "pending-confirmation" {
		tc["confirmed"] = "not-needed"
	}
	tc["status"] = "completed"
	for k, v := range result {
		tc[k] = v
	}
	h.chat(map[string]any{"type": "chat/toolCallComplete", "turnId": t.id, "toolCallId": b.ToolUseID, "result": result})
}

// answeredLocked closes the question a tool call asked, with what the TUI says was answered, if the chat did not answer it.
func (h *Host) answeredLocked(id string, ask askInput, record json.RawMessage, failed bool) {
	for _, p := range h.live.parts {
		request, _ := p["request"].(map[string]any)
		if p["kind"] != "inputRequest" || request == nil || request["id"] != id || p["response"] != nil {
			continue
		}
		response := "accept"
		if failed {
			response = "cancel"
		}
		p["response"] = response
		action := map[string]any{"type": "chat/inputCompleted", "turnId": h.live.id, "requestId": id, "response": response}
		if answers := answersOf(ask, record); answers != nil {
			action["answers"] = answers
			request["answers"] = answers
		}
		h.chat(action)
	}
}

func (h *Host) queueLocked(e entry) {
	if h.live == nil || strings.TrimSpace(e.Content) == "" {
		return
	}
	switch e.Operation {
	case "enqueue":
		if h.steering != nil && sameText(h.steering.Text, e.Content) {
			return // the hub's, typed by the Host
		}
		// typed in the terminal during the turn: Claude Code takes it at the turn's next step, as steering
		h.steering = &pendingMessage{ID: "steer-" + newID(), Text: e.Content}
		h.chat(map[string]any{"type": "chat/pendingMessageSet", "kind": "steering", "id": h.steering.ID, "message": userMessage(e.Content)})
	case "remove":
		if e.Reason == "absorbed_mid_turn" && h.steering != nil && sameText(h.steering.Text, e.Content) {
			h.chat(map[string]any{"type": "chat/pendingMessageRemoved", "kind": "steering", "id": h.steering.ID})
			h.steering = nil
		}
	}
}

// endLocked ends the turn that runs: complete, cancelled or error. duration 0 is measured, up to at when it is a timestamp.
func (h *Host) endLocked(state string, duration int64, at string) {
	t := h.live
	if t == nil {
		return
	}
	if duration <= 0 {
		end := h.cfg.Now()
		if parsed, err := time.Parse(time.RFC3339Nano, at); err == nil {
			end = parsed
		}
		duration = max(0, end.Sub(t.started).Milliseconds())
	}
	if state == "complete" && t.failure != "" {
		state = "error"
	}
	// what the reducer does to the tool calls and questions left open
	for _, p := range t.parts {
		if tc, ok := p["toolCall"].(map[string]any); ok && tc["status"] != "completed" && tc["status"] != "cancelled" {
			tc["status"], tc["reason"] = "cancelled", "skipped"
		}
	}
	switch state {
	case "complete":
		h.chat(map[string]any{"type": "chat/turnComplete", "turnId": t.id, "duration": duration})
	case "cancelled":
		h.chat(map[string]any{"type": "chat/turnCancelled", "turnId": t.id, "duration": duration})
	default:
		message := t.failure
		if message == "" {
			message = "Claude Code stopped on an error"
		}
		errorPart := map[string]any{"kind": "error", "error": map[string]any{"errorType": "ClaudeCodeError", "message": message}}
		h.chat(map[string]any{"type": "chat/error", "turnId": t.id, "duration": duration, "part": errorPart})
		t.parts = append(t.parts, errorPart)
	}
	h.turns = append(h.turns, t.json(state, duration))
	h.live = nil
	for id := range t.tools {
		h.dropWaitLocked(id)
	}
	// a steering message the turn did not take becomes the next turn's prompt in Claude Code: it waits for it. Otherwise the hub's next
	// queued message starts a turn now.
	if h.steering == nil && len(h.queued) > 0 {
		next := h.queued[0]
		h.queued = h.queued[1:]
		id := newID()
		h.typeTurnLocked(id, next.Text, map[string]any{"type": "chat/turnStarted", "turnId": id, "message": userMessage(next.Text),
			"queuedMessageId": next.ID}, nil)
	}
}

// ─── Hooks ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

// HookEvent is what Claude Code passes a hook on stdin, as far as the adapter reads it.
type HookEvent struct {
	Event          string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Source         string          `json:"source"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Error          json.RawMessage `json:"error"`
	ErrorDetails   string          `json:"error_details"`
}

// Started is SessionStart: the TUI started, or resumed. A turn the transcript left open ended with the TUI that ran it.
func (h *Host) Started(source string) {
	if source == "clear" || source == "compact" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.live != nil && h.live.bound {
		h.endLocked("cancelled", 0, "")
	}
}

// Stopped is Stop (failure "") or StopFailure: the turn is over. The transcript says so too, with its duration, but only once the Stop
// hooks are done: this one returns at once, and ends the turn itself only if the transcript has not a while later. A failure ends it now.
func (h *Host) Stopped(failure string) {
	h.drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.live == nil || !h.live.bound {
		return
	}
	if failure != "" {
		h.live.failure = failure
		h.endLocked("error", 0, "")
		return
	}
	id := h.live.id
	go func() {
		deadline := time.Now().Add(stopGrace)
		for time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			h.drain()
			h.mu.Lock()
			over := h.live == nil || h.live.id != id
			h.mu.Unlock()
			if over {
				return
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.live != nil && h.live.id == id {
			h.endLocked("complete", 0, "")
		}
	}()
}

// stopGrace is how long after a Stop hook the transcript has to end the turn.
var stopGrace = 5 * time.Second

// hookAllow and hookDeny are a PermissionRequest hook's answers.
func hookAllow(updatedInput map[string]any) []byte {
	decision := map[string]any{"behavior": "allow"}
	if updatedInput != nil {
		decision["updatedInput"] = updatedInput
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}})
	return out
}

func hookDeny(message string) []byte {
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest",
		"decision": map[string]any{"behavior": "deny", "message": message}}})
	return out
}

// findToolLocked is the tool call of the turn that runs a PermissionRequest is about: the hook names the tool and its input, not the call.
// The latest call of that tool that runs and was not asked about, the one with the same input first.
func (h *Host) findToolLocked(name string, input json.RawMessage) (string, map[string]any) {
	if h.live == nil {
		return "", nil
	}
	var want any
	_ = json.Unmarshal(input, &want)
	fallback := ""
	for i := len(h.live.parts) - 1; i >= 0; i-- {
		tc, ok := h.live.parts[i]["toolCall"].(map[string]any)
		if !ok || tc["toolName"] != name || tc["status"] != "running" {
			continue
		}
		id, _ := tc["toolCallId"].(string)
		if h.live.asked[id] {
			continue
		}
		var got any
		raw, _ := tc["toolInput"].(string)
		_ = json.Unmarshal([]byte(raw), &got)
		if reflect.DeepEqual(got, want) {
			return id, tc
		}
		if fallback == "" {
			fallback = id
		}
	}
	if fallback != "" {
		return fallback, h.live.tools[fallback]
	}
	return "", nil
}

// Permission is PermissionRequest: Claude Code shows its dialog in the TUI, and runs the hook at the same time. The tool call waits for
// an approval in the chat too (an AskUserQuestion for its answers); whichever comes first decides. done ends when Claude Code no longer
// waits for the hook (the person answered in the TUI, and the hook was stopped). The answer is the hook's output, nil for none.
func (h *Host) Permission(event HookEvent, done <-chan struct{}) []byte {
	deadline := time.After(3 * time.Second)
	h.mu.Lock()
	id, tc := h.findToolLocked(event.ToolName, event.ToolInput)
	for tc == nil {
		changed := h.changed
		h.mu.Unlock()
		h.drain()
		select {
		case <-changed:
		case <-deadline:
			return nil // not a tool call of the main session's turn (a subagent's): the TUI's dialog decides
		case <-done:
			return nil
		case <-time.After(200 * time.Millisecond):
		}
		h.mu.Lock()
		id, tc = h.findToolLocked(event.ToolName, event.ToolInput)
	}
	t := h.live
	t.asked[id] = true
	w := newWaiter()
	h.waits[id] = w
	var ask askInput
	question := event.ToolName == "AskUserQuestion"
	if question {
		request, parsed, ok := inputRequest(id, event.ToolInput)
		if !ok {
			h.dropWaitLocked(id)
			h.mu.Unlock()
			return nil
		}
		ask = parsed
		t.asks[id] = ask
		t.parts = append(t.parts, map[string]any{"kind": "inputRequest", "request": request})
		h.chat(map[string]any{"type": "chat/inputRequested", "turnId": t.id, "request": request})
	} else {
		display, _ := tc["displayName"].(string)
		tc["status"] = "pending-confirmation"
		tc["confirmationTitle"] = "Allow " + display + "?"
		delete(tc, "confirmed")
		h.chat(map[string]any{"type": "chat/toolCallReady", "turnId": t.id, "toolCallId": id, "invocationMessage": tc["invocationMessage"],
			"toolInput": tc["toolInput"], "confirmationTitle": tc["confirmationTitle"]})
	}
	h.mu.Unlock()

	select {
	case <-w.gone:
		return nil // answered in the TUI: the tool call has its result, or the turn ended
	case got := <-w.answer:
		if question {
			var response string
			_ = json.Unmarshal(got["response"], &response)
			if response != "accept" {
				return hookDeny("The person declined to answer, in egzo's chat.")
			}
			var answers map[string]json.RawMessage
			_ = json.Unmarshal(got["answers"], &answers)
			var input map[string]any
			_ = json.Unmarshal(event.ToolInput, &input)
			if input == nil {
				input = map[string]any{}
			}
			input["answers"] = answersFor(ask, answers)
			return hookAllow(input)
		}
		var approved bool
		_ = json.Unmarshal(got["approved"], &approved)
		if approved {
			return hookAllow(nil)
		}
		var why string
		_ = json.Unmarshal(got["reasonMessage"], &why)
		if why == "" {
			why = "The person denied this tool call, in egzo's chat."
		}
		return hookDeny(why)
	case <-done:
		// answered in the TUI: a tool call that still waits runs now (a denial comes as its failed result)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.waits[id] == w {
			h.dropWaitLocked(id)
		}
		if !question && h.live == t && tc["status"] == "pending-confirmation" {
			tc["status"], tc["confirmed"] = "running", "user-action"
			h.chat(map[string]any{"type": "chat/toolCallConfirmed", "turnId": t.id, "toolCallId": id, "approved": true, "confirmed": "user-action"})
		}
		return nil
	}
}

// ─── What clients ask ───────────────────────────────────────────────────────────────────────────────────────────────────────────────────

// Rejections, in the words opencode-ahp uses where it has them.
const (
	rejectBusy     = "a turn is already active; follow-up messages go via chat/pendingMessageSet"
	rejectNoTUI    = "Claude Code's terminal cannot be reached"
	rejectNoWait   = "nothing waits for that answer"
	rejectSteering = "Claude Code already has the steering message: it cannot be taken back"
)

// Dispatch is a client's chat/* action on the main chat (the link's filter lets no other through).
func (h *Host) Dispatch(action map[string]any, origin map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	str := func(key string) string {
		s, _ := action[key].(string)
		return s
	}
	reject := func(why string) { h.publish(h.main.Chat, action, origin, why) }
	text := func() string {
		m, _ := action["message"].(map[string]any)
		s, _ := m["text"].(string)
		return s
	}
	switch str("type") {
	case "chat/turnStarted":
		switch {
		case h.live != nil:
			reject(rejectBusy)
		case h.cfg.Typer == nil:
			reject(rejectNoTUI)
		case strings.TrimSpace(text()) == "" || str("turnId") == "":
			reject("a turn needs an id and a prompt")
		default:
			h.typeTurnLocked(str("turnId"), text(), action, origin)
		}
	case "chat/turnCancelled":
		if h.live == nil || h.live.id != str("turnId") {
			reject("that turn does not run")
			return
		}
		if !h.live.bound {
			h.endLocked("cancelled", 0, "")
			return
		}
		if h.cfg.Typer == nil {
			reject(rejectNoTUI)
			return
		}
		typer := h.cfg.Typer
		h.typing <- func() { _ = typer.Interrupt() } // the transcript says when Claude Code stopped
	case "chat/pendingMessageSet":
		kind, id, message := str("kind"), str("id"), text()
		switch {
		case h.cfg.Typer == nil:
			reject(rejectNoTUI)
		case id == "" || strings.TrimSpace(message) == "" || (kind != "steering" && kind != "queued"):
			reject("a pending message needs an id, a kind and a text")
		case h.live == nil:
			// no turn runs: it starts one
			h.publish(h.main.Chat, action, origin, "")
			turnID := newID()
			h.typeTurnLocked(turnID, message, map[string]any{"type": "chat/turnStarted", "turnId": turnID, "message": userMessage(message),
				"queuedMessageId": id}, nil)
		case kind == "steering":
			if h.steering != nil {
				reject("a steering message already waits for the turn's next step")
				return
			}
			h.steering = &pendingMessage{ID: id, Text: message, FromHub: true}
			h.publish(h.main.Chat, action, origin, "")
			typer := h.cfg.Typer
			h.typing <- func() { _ = typer.Prompt(message, false) }
		default:
			h.queued = append(h.queued, pendingMessage{ID: id, Text: message, FromHub: true})
			h.publish(h.main.Chat, action, origin, "")
		}
	case "chat/pendingMessageRemoved":
		if str("kind") == "steering" {
			reject(rejectSteering)
			return
		}
		for i, q := range h.queued {
			if q.ID == str("id") {
				h.queued = append(h.queued[:i:i], h.queued[i+1:]...)
				h.publish(h.main.Chat, action, origin, "")
				return
			}
		}
		reject("no such queued message")
	case "chat/toolCallConfirmed":
		id := str("toolCallId")
		wait := h.waits[id]
		tc := map[string]any(nil)
		if h.live != nil && h.live.id == str("turnId") {
			tc = h.live.tools[id]
		}
		if wait == nil || tc == nil || tc["status"] != "pending-confirmation" {
			reject(rejectNoWait)
			return
		}
		delete(h.waits, id)
		if approved, _ := action["approved"].(bool); approved {
			tc["status"], tc["confirmed"] = "running", "user-action"
		} else {
			tc["status"], tc["reason"] = "cancelled", "denied"
			if m := str("reasonMessage"); m != "" {
				tc["reasonMessage"] = m
			}
		}
		h.publish(h.main.Chat, action, origin, "")
		wait.answer <- raw(action)
	case "chat/inputCompleted":
		id := str("requestId")
		wait := h.waits[id]
		if wait == nil || h.live == nil {
			reject(rejectNoWait)
			return
		}
		delete(h.waits, id)
		for _, p := range h.live.parts {
			request, _ := p["request"].(map[string]any)
			if p["kind"] == "inputRequest" && request != nil && request["id"] == id && p["response"] == nil {
				p["response"] = str("response")
				if answers, ok := action["answers"]; ok {
					request["answers"] = answers
				}
			}
		}
		h.publish(h.main.Chat, action, origin, "")
		wait.answer <- raw(action)
	default:
		reject(str("type") + " is not supported by Claude Code's adapter")
	}
}

// typeTurnLocked starts a turn a client asked for (or the next queued message), and types its prompt into the TUI. The turn is live from
// now: the transcript binds it when Claude Code takes the prompt. One that could not be typed ends in an error.
func (h *Host) typeTurnLocked(id, text string, action, origin map[string]any) {
	h.startLocked(id, text, "", action, origin)
	typer := h.cfg.Typer
	if typer == nil {
		h.live.failure = rejectNoTUI
		h.endLocked("error", 0, "")
		return
	}
	h.typing <- func() {
		if err := typer.Prompt(text, true); err != nil {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.live != nil && h.live.id == id && !h.live.bound {
				h.live.failure = "The prompt could not be typed into Claude Code: " + err.Error()
				h.endLocked("error", 0, "")
			}
		}
	}
}

func raw(action map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range action {
		out[k], _ = json.Marshal(v)
	}
	return out
}

// newID is a random UUID.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	x := hex.EncodeToString(b)
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}
