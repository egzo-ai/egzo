// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The transcript entries below have the shapes Claude Code 2.1.329 writes (recorded against its TUI), cut down to what the adapter reads.

const testSession = "53da7c51-6ec3-4ebf-ace4-de6ab06841d6"

func ts(sec int) string { return fmt.Sprintf("2026-11-28T05:00:%02d.000Z", sec) }

func line(v map[string]any) []byte {
	v["sessionId"] = testSession
	out, _ := json.Marshal(v)
	return out
}

func prompt(uuid, text string, sec int) []byte {
	return line(map[string]any{"type": "user", "uuid": uuid, "timestamp": ts(sec), "promptSource": "typed",
		"message": map[string]any{"role": "user", "content": text}})
}

func assistant(uuid, msgID string, sec int, blocks ...map[string]any) []byte {
	content := []any{}
	for _, b := range blocks {
		content = append(content, b)
	}
	return line(map[string]any{"type": "assistant", "uuid": uuid, "timestamp": ts(sec), "message": map[string]any{"id": msgID, "role": "assistant",
		"model": "claude-opus-5-5", "content": content,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 10}}})
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(uuid, id, content string, isError bool, sec int, record any) []byte {
	v := map[string]any{"type": "user", "uuid": uuid, "timestamp": ts(sec), "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isError}}}}
	if record != nil {
		v["toolUseResult"] = record
	}
	return line(v)
}

func turnDuration(ms int64, sec int) []byte {
	return line(map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": ms, "timestamp": ts(sec)})
}

func interrupt(sec int) []byte {
	return line(map[string]any{"type": "user", "timestamp": ts(sec), "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"}}}})
}

func queueOp(op, content, reason string, sec int) []byte {
	return line(map[string]any{"type": "queue-operation", "operation": op, "content": content, "reason": reason, "timestamp": ts(sec)})
}

// typer records what the Host types into the TUI.
type typer struct {
	mu        sync.Mutex
	prompts   []string
	idle      []bool
	interrupt int
	fail      error
}

func (t *typer) Prompt(text string, idle bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prompts = append(t.prompts, text)
	t.idle = append(t.idle, idle)
	return t.fail
}

func (t *typer) Interrupt() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.interrupt++
	return nil
}

func (t *typer) typed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.prompts...)
}

func newHost(t *testing.T) (*Host, *typer) {
	t.Helper()
	ty := &typer{}
	h := New(Config{Session: testSession, Typer: ty, Now: func() time.Time { return time.Date(2026, 11, 28, 5, 1, 0, 0, time.UTC) }})
	return h, ty
}

// actions is every action the Host published, with its envelope.
func actions(h *Host) []envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]envelope{}, h.log...)
}

func types(h *Host) []string {
	var out []string
	for _, e := range actions(h) {
		t, _ := e.Action["type"].(string)
		if e.RejectionReason != "" {
			t += " (rejected)"
		}
		out = append(out, t)
	}
	return out
}

func chatState(t *testing.T, h *Host) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	snap := h.snapshotLocked(h.main.Chat)
	// through JSON, as a client gets it
	data, _ := json.Marshal(snap["state"])
	var state map[string]any
	_ = json.Unmarshal(data, &state)
	return state
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestATurnOfTheTranscriptIsATurnOfTheChat(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "run the tests", 1))
	h.Apply(assistant("a1", "msg_1", 2, map[string]any{"type": "thinking", "thinking": "I should run them."}))
	h.Apply(assistant("a2", "msg_1", 2, text("Running them.")))
	h.Apply(assistant("a3", "msg_1", 2, toolUse("toolu_1", "Bash", map[string]any{"command": "go test ./...", "description": "Run the tests"})))
	h.Apply(toolResult("u2", "toolu_1", "ok\tegzo\t0.4s", false, 3, nil))
	h.Apply(assistant("a4", "msg_2", 4, text("They **pass**.")))
	h.Apply(turnDuration(3200, 5))

	got := strings.Join(types(h), " ")
	want := "chat/turnStarted session/titleChanged chat/usage chat/responsePart chat/responsePart chat/toolCallStart chat/toolCallReady " +
		"chat/toolCallComplete chat/usage chat/responsePart chat/turnComplete"
	if got != want {
		t.Fatalf("actions:\n got %s\nwant %s", got, want)
	}
	state := chatState(t, h)
	turns := state["turns"].([]any)
	if len(turns) != 1 || state["activeTurn"] != nil {
		t.Fatalf("state = %v", state)
	}
	turn := turns[0].(map[string]any)
	if turn["id"] != "turn-u1" || turn["state"] != "complete" || turn["duration"] != float64(3200) {
		t.Errorf("turn = %v", turn)
	}
	if turn["message"].(map[string]any)["text"] != "run the tests" {
		t.Errorf("message = %v", turn["message"])
	}
	parts := turn["responseParts"].([]any)
	kinds := []string{}
	for _, p := range parts {
		kinds = append(kinds, p.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "reasoning,markdown,toolCall,markdown" {
		t.Errorf("parts = %v", kinds)
	}
	tc := parts[2].(map[string]any)["toolCall"].(map[string]any)
	if tc["status"] != "completed" || tc["success"] != true || tc["displayName"] != "Shell" || tc["pastTenseMessage"] != "Ran `go test ./...`" ||
		tc["toolInput"] != `{"command":"go test ./...","description":"Run the tests"}` {
		t.Errorf("tool call = %v", tc)
	}
	// usage counts each API message once: two messages, 20 output tokens each; the input is the last message's context
	usage := turn["usage"].(map[string]any)
	if usage["outputTokens"] != float64(40) || usage["inputTokens"] != float64(160) || usage["cacheReadTokens"] != float64(50) ||
		usage["model"] != "claude-opus-5-5" {
		t.Errorf("usage = %v", usage)
	}
	if state["title"] != "run the tests" {
		t.Errorf("the first prompt titles the testSession until Claude Code names it: %v", state["title"])
	}
	h.Apply(line(map[string]any{"type": "ai-title", "aiTitle": "Run the test suite"}))
	if chatState(t, h)["title"] != "Run the test suite" {
		t.Error("Claude Code's title is taken")
	}
}

func TestWhatIsNotTheChatIsLeftOut(t *testing.T) {
	h, _ := newHost(t)
	for _, l := range [][]byte{
		line(map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": "Caveat: ..."}}),
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<command-name>/model</command-name>"}}),
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<local-command-stdout>Set model</local-command-stdout>"}}),
		line(map[string]any{"type": "user", "isSidechain": true, "message": map[string]any{"role": "user", "content": "a subagent's prompt"}}),
		line(map[string]any{"type": "user", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "Summary..."}}),
		line(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "date", "date": "2026-11-28"}}),
		line(map[string]any{"type": "permission-mode", "permissionMode": "default"}),
		[]byte("not json"),
	} {
		h.Apply(l)
	}
	if got := types(h); len(got) != 0 {
		t.Errorf("actions = %v", got)
	}
}

func TestAnInterruptCancelsTheTurnAndAnAPIErrorFailsIt(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_1", "Bash", map[string]any{"command": "sleep 60"})))
	h.Apply(toolResult("u2", "toolu_1", "The user doesn't want to proceed with this tool use.", true, 4, nil))
	h.Apply(interrupt(4))
	h.Apply(prompt("u3", "say hello", 6))
	h.Apply(line(map[string]any{"type": "assistant", "uuid": "a2", "timestamp": ts(7), "isApiErrorMessage": true,
		"message": map[string]any{"id": "e1", "role": "assistant", "content": []any{text("API Error: 529 Overloaded")}}}))
	h.Apply(turnDuration(900, 8))

	turns := chatState(t, h)["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("turns = %v", turns)
	}
	first, second := turns[0].(map[string]any), turns[1].(map[string]any)
	if first["state"] != "cancelled" || first["duration"] != float64(3000) {
		t.Errorf("first = %v", first)
	}
	if second["state"] != "error" {
		t.Fatalf("second = %v", second)
	}
	parts := second["responseParts"].([]any)
	last := parts[len(parts)-1].(map[string]any)
	if last["kind"] != "error" || !strings.Contains(fmt.Sprint(last["error"]), "529 Overloaded") {
		t.Errorf("the error is the turn's last part: %v", parts)
	}
}

func TestAPromptFromAClientIsTypedAndTheTranscriptBindsIt(t *testing.T) {
	h, ty := newHost(t)
	origin := map[string]any{"clientId": "hub", "clientSeq": 1}
	h.Dispatch(map[string]any{"type": "chat/turnStarted", "turnId": "hub-1", "message": map[string]any{"text": "say hello", "origin": map[string]any{"kind": "user"}}}, origin)
	eventually(t, "the prompt is typed", func() bool { return len(ty.typed()) == 1 })
	if ty.typed()[0] != "say hello" || !ty.idle[0] {
		t.Errorf("typed %v %v", ty.prompts, ty.idle)
	}
	log := actions(h)
	if len(log) < 1 || log[0].Action["type"] != "chat/turnStarted" || log[0].Action["turnId"] != "hub-1" || log[0].Origin["clientId"] != "hub" {
		t.Fatalf("the client's turn is echoed with its origin: %v", log)
	}
	// a second prompt while it runs is refused, as opencode-ahp refuses it
	h.Dispatch(map[string]any{"type": "chat/turnStarted", "turnId": "hub-2", "message": map[string]any{"text": "and this"}}, origin)
	// Claude Code takes the prompt: it is the same turn, not a new one
	h.Apply(prompt("u1", "say hello", 1))
	h.Apply(assistant("a1", "msg_1", 2, text("Hello!")))
	h.Apply(turnDuration(500, 3))
	got := strings.Join(types(h), " ")
	if got != "chat/turnStarted session/titleChanged chat/turnStarted (rejected) chat/usage chat/responsePart chat/turnComplete" {
		t.Fatalf("actions = %s", got)
	}
	turns := chatState(t, h)["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["id"] != "hub-1" {
		t.Errorf("turns = %v", turns)
	}
}

func TestAPromptThatCannotBeTypedEndsInAnError(t *testing.T) {
	h, ty := newHost(t)
	ty.fail = fmt.Errorf("no terminal")
	h.Dispatch(map[string]any{"type": "chat/turnStarted", "turnId": "hub-1", "message": map[string]any{"text": "say hello"}}, nil)
	eventually(t, "the turn fails", func() bool { return len(chatState(t, h)["turns"].([]any)) == 1 })
	turn := chatState(t, h)["turns"].([]any)[0].(map[string]any)
	if turn["state"] != "error" || !strings.Contains(fmt.Sprint(turn["responseParts"]), "no terminal") {
		t.Errorf("turn = %v", turn)
	}
}

// Claude Code writes turn_duration once the Stop hooks are done; a prompt typed meanwhile must not be ended by it.
func TestALateEndOfTheTurnBeforeDoesNotEndTheNextOne(t *testing.T) {
	h, _ := newHost(t)
	stopGrace = 50 * time.Millisecond
	defer func() { stopGrace = 5 * time.Second }()
	h.Apply(prompt("u1", "first", 1))
	h.Apply(assistant("a1", "msg_1", 2, text("one")))
	h.Hook(HookEvent{Event: "Stop", SessionID: testSession}, nil)
	eventually(t, "the Stop hook ends the turn the transcript did not", func() bool { return chatState(t, h)["activeTurn"] == nil })
	// the hub's next prompt, unbound, then the late end of the first: nothing ends
	h.Dispatch(map[string]any{"type": "chat/turnStarted", "turnId": "hub-2", "message": map[string]any{"text": "second"}}, nil)
	h.Apply(turnDuration(1000, 3))
	if chatState(t, h)["activeTurn"] == nil {
		t.Fatal("a turn_duration ended a turn Claude Code had not taken yet")
	}
	// bound now, with a prompt after the late end: still nothing
	h.Apply(prompt("u2", "second", 4))
	h.Apply(turnDuration(1000, 3))
	if chatState(t, h)["activeTurn"] == nil {
		t.Fatal("a turn_duration from before the prompt ended the turn")
	}
	h.Apply(turnDuration(700, 6))
	if chatState(t, h)["activeTurn"] != nil {
		t.Fatal("its own end ends it")
	}
}

func TestSteeringFromAClientIsTypedAndTakenAtTheNextStep(t *testing.T) {
	h, ty := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Dispatch(map[string]any{"type": "chat/pendingMessageSet", "kind": "steering", "id": "s1",
		"message": map[string]any{"text": "also mention cats"}}, map[string]any{"clientId": "hub"})
	eventually(t, "it is typed", func() bool { return len(ty.typed()) == 1 })
	if ty.idle[0] {
		t.Error("steering is typed into a turn that runs: no waiting for the prompt box")
	}
	if chatState(t, h)["steeringMessage"] == nil {
		t.Fatal("it waits as the steering message")
	}
	h.Apply(queueOp("enqueue", "also mention cats", "", 2)) // the Host's own: no new steering message
	h.Apply(queueOp("remove", "also mention cats", "absorbed_mid_turn", 3))
	got := types(h)
	if strings.Join(got, " ") != "chat/turnStarted session/titleChanged chat/pendingMessageSet chat/pendingMessageRemoved" {
		t.Fatalf("actions = %v", got)
	}
	if last := actions(h)[3].Action; last["kind"] != "steering" || last["id"] != "s1" {
		t.Errorf("removed = %v", last)
	}
	if chatState(t, h)["steeringMessage"] != nil {
		t.Error("taken")
	}
}

func TestSteeringTypedInTheTerminalIsShownToo(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Apply(queueOp("enqueue", "use tabs", "", 2))
	steering, _ := chatState(t, h)["steeringMessage"].(map[string]any)
	if steering == nil || steering["message"].(map[string]any)["text"] != "use tabs" {
		t.Fatalf("steering = %v", steering)
	}
	h.Apply(queueOp("remove", "use tabs", "absorbed_mid_turn", 3))
	if chatState(t, h)["steeringMessage"] != nil {
		t.Error("taken")
	}
}

// A steering message the turn did not take: Claude Code runs it as the next prompt.
func TestSteeringNotTakenStartsTheNextTurn(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Apply(queueOp("enqueue", "use tabs", "", 2))
	h.Apply(turnDuration(1000, 3))
	h.Apply(prompt("u2", "use tabs", 4))
	log := actions(h)
	last := log[len(log)-1].Action
	if last["type"] != "chat/turnStarted" || last["queuedMessageId"] == nil {
		t.Fatalf("the next turn starts from the steering message: %v", last)
	}
	if chatState(t, h)["steeringMessage"] != nil {
		t.Error("it is no longer pending")
	}
}

func TestAQueuedMessageWaitsForTheTurnAndThenStartsOne(t *testing.T) {
	h, ty := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Dispatch(map[string]any{"type": "chat/pendingMessageSet", "kind": "queued", "id": "q1", "message": map[string]any{"text": "then this"}}, nil)
	h.Dispatch(map[string]any{"type": "chat/pendingMessageSet", "kind": "queued", "id": "q2", "message": map[string]any{"text": "not this"}}, nil)
	h.Dispatch(map[string]any{"type": "chat/pendingMessageRemoved", "kind": "queued", "id": "q2"}, nil)
	if len(ty.typed()) != 0 {
		t.Fatal("nothing is typed while the turn runs")
	}
	queued := chatState(t, h)["queuedMessages"].([]any)
	if len(queued) != 1 {
		t.Fatalf("queued = %v", queued)
	}
	h.Apply(turnDuration(1000, 3))
	eventually(t, "the queued message is typed", func() bool { return len(ty.typed()) == 1 })
	if ty.typed()[0] != "then this" {
		t.Errorf("typed %v", ty.typed())
	}
	state := chatState(t, h)
	active, _ := state["activeTurn"].(map[string]any)
	if active == nil || state["queuedMessages"] != nil {
		t.Fatalf("state = %v", state)
	}
	log := actions(h)
	if started := log[len(log)-1].Action; started["type"] != "chat/turnStarted" || started["queuedMessageId"] != "q1" {
		t.Errorf("started = %v", started)
	}
}

func TestCancellingTypesEscAndTheTranscriptSaysWhenItStopped(t *testing.T) {
	h, ty := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Dispatch(map[string]any{"type": "chat/turnCancelled", "turnId": "turn-u1"}, nil)
	eventually(t, "Esc", func() bool { ty.mu.Lock(); defer ty.mu.Unlock(); return ty.interrupt == 1 })
	if chatState(t, h)["activeTurn"] == nil {
		t.Fatal("the turn runs until Claude Code says it stopped")
	}
	h.Apply(interrupt(2))
	if chatState(t, h)["activeTurn"] != nil {
		t.Fatal("cancelled")
	}
	h.Dispatch(map[string]any{"type": "chat/turnCancelled", "turnId": "turn-u1"}, nil)
	if got := types(h); got[len(got)-1] != "chat/turnCancelled (rejected)" {
		t.Errorf("a turn that does not run cannot be cancelled: %v", got)
	}
}

func permission(h *Host, name string, input map[string]any) (chan []byte, chan struct{}) {
	raw, _ := json.Marshal(input)
	out, done := make(chan []byte, 1), make(chan struct{})
	go func() { out <- h.Hook(HookEvent{Event: "PermissionRequest", SessionID: testSession, ToolName: name, ToolInput: raw}, done) }()
	return out, done
}

func toolCall(t *testing.T, h *Host, id string) map[string]any {
	t.Helper()
	active, _ := chatState(t, h)["activeTurn"].(map[string]any)
	if active == nil {
		return nil
	}
	for _, p := range active["responseParts"].([]any) {
		if tc, ok := p.(map[string]any)["toolCall"].(map[string]any); ok && tc["toolCallId"] == id {
			return tc
		}
	}
	return nil
}

func TestAToolCallWaitsForAnApprovalInTheChat(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(fmt.Sprint("approved=", approved), func(t *testing.T) {
			h, _ := newHost(t)
			h.Apply(prompt("u1", "clean up", 1))
			h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_1", "Bash", map[string]any{"command": "rm -rf build"})))
			out, _ := permission(h, "Bash", map[string]any{"command": "rm -rf build"})
			eventually(t, "it waits", func() bool { tc := toolCall(t, h, "toolu_1"); return tc != nil && tc["status"] == "pending-confirmation" })
			if s := chatState(t, h)["status"]; s != float64(statusInputNeeded) {
				t.Errorf("the chat waits for the person: status %v", s)
			}
			action := map[string]any{"type": "chat/toolCallConfirmed", "turnId": "turn-u1", "toolCallId": "toolu_1", "approved": approved}
			if approved {
				action["confirmed"] = "user-action"
			} else {
				action["reason"], action["reasonMessage"] = "denied", "not the build dir"
			}
			h.Dispatch(action, map[string]any{"clientId": "hub"})
			answer := string(<-out)
			if approved && (!strings.Contains(answer, `"behavior":"allow"`) || !strings.Contains(answer, `"hookEventName":"PermissionRequest"`)) {
				t.Errorf("answer = %s", answer)
			}
			if !approved && (!strings.Contains(answer, `"behavior":"deny"`) || !strings.Contains(answer, "not the build dir")) {
				t.Errorf("answer = %s", answer)
			}
			want := map[bool]string{true: "running", false: "cancelled"}[approved]
			if tc := toolCall(t, h, "toolu_1"); tc["status"] != want {
				t.Errorf("tool call = %v", tc)
			}
			// a second answer has nothing to answer
			h.Dispatch(action, nil)
			if got := types(h); got[len(got)-1] != "chat/toolCallConfirmed (rejected)" {
				t.Errorf("actions = %v", got)
			}
		})
	}
}

func TestAnApprovalGivenInTheTerminalLetsTheHookGo(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "clean up", 1))
	h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_1", "Bash", map[string]any{"command": "rm -rf build"})))
	out, _ := permission(h, "Bash", map[string]any{"command": "rm -rf build"})
	eventually(t, "it waits", func() bool { tc := toolCall(t, h, "toolu_1"); return tc != nil && tc["status"] == "pending-confirmation" })
	// approved in the TUI: the tool ran, its result is in the transcript
	h.Apply(toolResult("u2", "toolu_1", "", false, 3, nil))
	select {
	case answer := <-out:
		if answer != nil {
			t.Errorf("the TUI decided: no answer, got %s", answer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook still waits")
	}
	if tc := toolCall(t, h, "toolu_1"); tc["status"] != "completed" {
		t.Errorf("tool call = %v", tc)
	}
}

func TestAPermissionRequestOfAnotherSessionOrUnknownCallIsTheTUIs(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "clean up", 1))
	raw, _ := json.Marshal(map[string]any{"command": "ls"})
	if out := h.Hook(HookEvent{Event: "PermissionRequest", SessionID: "other", ToolName: "Bash", ToolInput: raw}, nil); out != nil {
		t.Errorf("another testSession's: %s", out)
	}
	start := time.Now()
	if out := h.Hook(HookEvent{Event: "PermissionRequest", SessionID: testSession, ToolName: "Bash", ToolInput: raw}, nil); out != nil {
		t.Errorf("a call the transcript does not have (a subagent's): %s", out)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("it is not waited for long")
	}
}

func TestAQuestionIsAskedInTheChatAndItsAnswersGoBackToClaudeCode(t *testing.T) {
	h, _ := newHost(t)
	input := map[string]any{"questions": []any{
		map[string]any{"question": "Which color?", "header": "Color", "multiSelect": false,
			"options": []any{map[string]any{"label": "Red", "description": "warm"}, map[string]any{"label": "Blue", "description": "cool"}}},
		map[string]any{"question": "Which sizes?", "header": "Size", "multiSelect": true,
			"options": []any{map[string]any{"label": "S"}, map[string]any{"label": "M"}, map[string]any{"label": "L"}}},
	}}
	h.Apply(prompt("u1", "ask me", 1))
	h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_q", "AskUserQuestion", input)))
	out, _ := permission(h, "AskUserQuestion", input)
	eventually(t, "it is asked", func() bool { return chatState(t, h)["status"] == float64(statusInputNeeded) })
	var request map[string]any
	for _, e := range actions(h) {
		if e.Action["type"] == "chat/inputRequested" {
			request = e.Action["request"].(map[string]any)
		}
	}
	questions, _ := request["questions"].([]any)
	if request["id"] != "toolu_q" || len(questions) != 2 {
		t.Fatalf("request = %v", request)
	}
	q0, q1 := questions[0].(map[string]any), questions[1].(map[string]any)
	if q0["kind"] != "single-select" || q0["title"] != "Color" || q0["message"] != "Which color?" || q1["kind"] != "multi-select" {
		t.Errorf("questions = %v", questions)
	}
	h.Dispatch(map[string]any{"type": "chat/inputCompleted", "requestId": "toolu_q", "response": "accept", "answers": map[string]any{
		"q0": map[string]any{"state": "submitted", "value": map[string]any{"kind": "selected", "value": "q0o1"}},
		"q1": map[string]any{"state": "submitted", "value": map[string]any{"kind": "selected-many", "value": []any{"q1o0", "q1o2"}, "freeformValues": []any{"XL"}}},
	}}, map[string]any{"clientId": "hub"})
	var answer struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior     string `json:"behavior"`
				UpdatedInput struct {
					Questions []any             `json:"questions"`
					Answers   map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(<-out, &answer); err != nil {
		t.Fatal(err)
	}
	d := answer.HookSpecificOutput.Decision
	if d.Behavior != "allow" || len(d.UpdatedInput.Questions) != 2 || d.UpdatedInput.Answers["Which color?"] != "Blue" ||
		d.UpdatedInput.Answers["Which sizes?"] != "S, L, XL" {
		t.Errorf("decision = %+v", d)
	}
}

func TestAQuestionAnsweredInTheTerminalIsShownAnswered(t *testing.T) {
	h, _ := newHost(t)
	input := map[string]any{"questions": []any{map[string]any{"question": "Which color?", "header": "Color",
		"options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Blue"}}}}}
	h.Apply(prompt("u1", "ask me", 1))
	h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_q", "AskUserQuestion", input)))
	out, _ := permission(h, "AskUserQuestion", input)
	eventually(t, "it is asked", func() bool { return chatState(t, h)["status"] == float64(statusInputNeeded) })
	h.Apply(toolResult("u2", "toolu_q", `Your questions have been answered: "Which color?"="Red".`, false, 3,
		map[string]any{"questions": input["questions"], "answers": map[string]any{"Which color?": "Red"}}))
	if answer := <-out; answer != nil {
		t.Errorf("answer = %s", answer)
	}
	var completed map[string]any
	for _, e := range actions(h) {
		if e.Action["type"] == "chat/inputCompleted" {
			completed = e.Action
		}
	}
	if completed == nil || completed["response"] != "accept" || !strings.Contains(fmt.Sprint(completed["answers"]), "q0o0") {
		t.Errorf("completed = %v", completed)
	}
	if chatState(t, h)["status"] != float64(statusInProgress) {
		t.Error("the chat no longer waits")
	}
}

func TestARestartOfTheTUIEndsTheTurnItLeftOpen(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "slow work", 1))
	h.Hook(HookEvent{Event: "SessionStart", SessionID: testSession, Source: "resume"}, nil)
	turns := chatState(t, h)["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["state"] != "cancelled" {
		t.Errorf("turns = %v", turns)
	}
}

func TestAStopFailureEndsTheTurnInAnError(t *testing.T) {
	h, _ := newHost(t)
	h.Apply(prompt("u1", "hello", 1))
	h.Hook(HookEvent{Event: "StopFailure", SessionID: testSession, Error: json.RawMessage(`"rate_limit"`), ErrorDetails: "429 Too Many Requests"}, nil)
	turns := chatState(t, h)["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["state"] != "error" || !strings.Contains(fmt.Sprint(turns[0]), "429") {
		t.Errorf("turns = %v", turns)
	}
}

func TestActionsTheAdapterDoesNotTakeAreRejected(t *testing.T) {
	h, _ := newHost(t)
	h.Dispatch(map[string]any{"type": "chat/draftChanged", "draft": "x"}, nil)
	h.Dispatch(map[string]any{"type": "chat/inputCompleted", "requestId": "nothing", "response": "accept"}, nil)
	h.Apply(prompt("u1", "hello", 1))
	h.Dispatch(map[string]any{"type": "chat/pendingMessageSet", "kind": "steering", "id": "s1", "message": map[string]any{"text": "a"}}, nil)
	h.Dispatch(map[string]any{"type": "chat/pendingMessageRemoved", "kind": "steering", "id": "s1"}, nil)
	var rejected []string
	for _, e := range actions(h) {
		if e.RejectionReason != "" {
			rejected = append(rejected, e.Action["type"].(string))
		}
	}
	if strings.Join(rejected, " ") != "chat/draftChanged chat/inputCompleted chat/pendingMessageRemoved" {
		t.Errorf("rejected = %v", rejected)
	}
}
