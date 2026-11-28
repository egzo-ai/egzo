// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/ahp"
)

const testToken = "secret-token"

// hub is a client of the adapter the way the hub is: through `egzo agent ahp` (ahp.Bridge) and the link's filter.
type hub struct {
	t      *testing.T
	in     *io.PipeWriter
	lines  chan map[string]any
	filter *ahp.Filter
	next   int
	// backlog is what arrived while waiting for something else
	backlog []map[string]any
}

func connect(t *testing.T, h *Host, readOnly bool) *hub {
	t.Helper()
	server := httptest.NewServer(h.Handler(testToken, Path, HookPath))
	t.Cleanup(server.Close)
	mainFile := filepath.Join(t.TempDir(), "main-session")
	os.WriteFile(mainFile, []byte(testSession+"\n"), 0o644)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + Path
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); inW.Close() })
	go func() { _ = ahp.Bridge(ctx, url, testToken, mainFile, inR, outW); outW.Close() }()
	c := &hub{t: t, in: inW, lines: make(chan map[string]any, 1000), filter: &ahp.Filter{ReadOnly: readOnly}}
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 64<<10), ahp.MaxMessage)
		for scanner.Scan() {
			passed := c.filter.FromServer(scanner.Bytes())
			if passed == nil {
				continue
			}
			var m map[string]any
			if json.Unmarshal(passed, &m) == nil {
				c.lines <- m
			}
		}
		close(c.lines)
	}()
	return c
}

// send passes a line through the filter, as the link does; a refused request is answered by the filter.
func (c *hub) send(m map[string]any) {
	data, _ := json.Marshal(m)
	forward, reply := c.filter.FromClient(data)
	if reply != nil {
		var r map[string]any
		_ = json.Unmarshal(reply, &r)
		c.lines <- r
	}
	if forward != nil {
		c.in.Write(append(forward, '\n'))
	}
}

func (c *hub) ask(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.next++
	id := float64(c.next)
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params})
	return c.wait(method, func(m map[string]any) bool { return m["id"] == id })
}

func (c *hub) wait(what string, match func(map[string]any) bool) map[string]any {
	c.t.Helper()
	for i, m := range c.backlog {
		if match(m) {
			c.backlog = append(c.backlog[:i:i], c.backlog[i+1:]...)
			return m
		}
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-c.lines:
			if !ok {
				c.t.Fatalf("closed while waiting for %s", what)
			}
			if match(m) {
				return m
			}
			c.backlog = append(c.backlog, m)
		case <-timeout:
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (c *hub) action(kind string) map[string]any {
	c.t.Helper()
	return c.wait(kind, func(m map[string]any) bool {
		params, _ := m["params"].(map[string]any)
		action, _ := params["action"].(map[string]any)
		return m["method"] == "action" && action["type"] == kind
	})["params"].(map[string]any)
}

var initialize = map[string]any{"channel": ahp.RootChannel, "clientId": "hub-1", "protocolVersions": []any{"1.0.0"}, "initialSubscriptions": []any{ahp.RootChannel}}

func TestTheHubReachesTheChatThroughTheBridgeAndTheFilter(t *testing.T) {
	h, ty := newHost(t)
	h.Apply(prompt("u1", "explain the parser", 1))
	h.Apply(assistant("a1", "msg_1", 2, text("It is recursive descent.")))
	h.Apply(turnDuration(800, 3))
	c := connect(t, h, false)

	result := c.ask("initialize", initialize)["result"].(map[string]any)
	if result["protocolVersion"] != "1.0.0" || result["serverInfo"].(map[string]any)["name"] != "egzo-claude-ahp" {
		t.Errorf("initialize = %v", result)
	}
	note := c.wait("the main session", func(m map[string]any) bool { return m["method"] == ahp.MainSessionMethod })
	main := ahp.SessionURIs(testSession)
	if p := note["params"].(map[string]any); p["session"] != main.Session || p["chat"] != main.Chat {
		t.Fatalf("main session = %v", p)
	}
	list := c.ask("listSessions", map[string]any{})["result"].(map[string]any)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["resource"] != main.Session || list[0].(map[string]any)["provider"] != "claude-code" {
		t.Errorf("sessions = %v", list)
	}
	snap := c.ask("subscribe", map[string]any{"channel": main.Chat})["result"].(map[string]any)["snapshot"].(map[string]any)
	turns := snap["state"].(map[string]any)["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["message"].(map[string]any)["text"] != "explain the parser" {
		t.Fatalf("snapshot = %v", snap)
	}
	c.ask("subscribe", map[string]any{"channel": main.Session})

	// a prompt: echoed with the client's origin, typed into the TUI
	c.send(map[string]any{"jsonrpc": "2.0", "method": "dispatchAction", "params": map[string]any{"channel": main.Chat, "clientSeq": 1,
		"action": map[string]any{"type": "chat/turnStarted", "turnId": "t-2", "message": map[string]any{"text": "and the lexer?", "origin": map[string]any{"kind": "user"}}}}})
	started := c.action("chat/turnStarted")
	if started["action"].(map[string]any)["turnId"] != "t-2" || started["origin"].(map[string]any)["clientId"] != "hub-1" {
		t.Errorf("started = %v", started)
	}
	eventually(t, "typed", func() bool { return len(ty.typed()) == 1 })
	h.Apply(prompt("u2", "and the lexer?", 4))
	h.Apply(assistant("a2", "msg_2", 5, text("A table.")))
	h.Apply(turnDuration(500, 6))
	c.action("chat/responsePart")
	c.action("chat/turnComplete")

	// what the filter refuses never reaches the adapter
	refused := c.ask("resourceRead", map[string]any{"uri": "file:///etc/passwd"})
	if refused["error"] == nil {
		t.Errorf("resourceRead = %v", refused)
	}
	// and what it lets through that the adapter does not serve is an error, not silence
	if got := c.ask("subscribe", map[string]any{"channel": ahp.RootChannel})["result"]; got == nil {
		t.Error("the root channel can be subscribed to")
	}
}

func TestAClientThatReconnectsGetsWhatItMissed(t *testing.T) {
	h, _ := newHost(t)
	main := ahp.SessionURIs(testSession)
	first := connect(t, h, false)
	first.ask("initialize", initialize)
	first.wait("main", func(m map[string]any) bool { return m["method"] == ahp.MainSessionMethod })
	snap := first.ask("subscribe", map[string]any{"channel": main.Chat})["result"].(map[string]any)["snapshot"].(map[string]any)
	seen := int(snap["fromSeq"].(float64))
	h.Apply(prompt("u1", "while away", 1))
	h.Apply(assistant("a1", "msg_1", 2, text("done")))

	again := connect(t, h, false)
	again.wait("main", func(m map[string]any) bool { return m["method"] == ahp.MainSessionMethod })
	result := again.ask("reconnect", map[string]any{"channel": ahp.RootChannel, "clientId": "hub-1", "lastSeenServerSeq": seen,
		"subscriptions": []any{main.Chat}})["result"].(map[string]any)
	if result["type"] != "replay" {
		t.Fatalf("a known client gets a replay: %v", result)
	}
	kinds := []string{}
	for _, a := range result["actions"].([]any) {
		kinds = append(kinds, a.(map[string]any)["action"].(map[string]any)["type"].(string))
	}
	if strings.Join(kinds, " ") != "chat/turnStarted chat/usage chat/responsePart" {
		t.Errorf("replayed = %v", kinds)
	}
	stranger := connect(t, h, false)
	stranger.wait("main", func(m map[string]any) bool { return m["method"] == ahp.MainSessionMethod })
	result = stranger.ask("reconnect", map[string]any{"channel": ahp.RootChannel, "clientId": "someone-else", "lastSeenServerSeq": 0,
		"subscriptions": []any{main.Chat}})["result"].(map[string]any)
	if result["type"] != "snapshot" || len(result["snapshots"].([]any)) != 1 {
		t.Errorf("an unknown client gets snapshots: %v", result)
	}
}

func TestAWatchingClientCannotActInTheChat(t *testing.T) {
	h, ty := newHost(t)
	c := connect(t, h, true)
	c.ask("initialize", initialize)
	c.wait("main", func(m map[string]any) bool { return m["method"] == ahp.MainSessionMethod })
	answer := c.ask("dispatchAction", map[string]any{"channel": ahp.SessionURIs(testSession).Chat, "clientSeq": 1,
		"action": map[string]any{"type": "chat/turnStarted", "turnId": "t", "message": map[string]any{"text": "hi"}}})
	if answer["error"] == nil {
		t.Errorf("answer = %v", answer)
	}
	time.Sleep(100 * time.Millisecond)
	if len(ty.typed()) != 0 {
		t.Error("nothing is typed for a client that watches")
	}
}

func TestTheServerAsksForTheToken(t *testing.T) {
	h, _ := newHost(t)
	server := httptest.NewServer(h.Handler(testToken, Path, HookPath))
	defer server.Close()
	for _, path := range []string{Path, HookPath} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(`{"hook_event_name":"Stop"}`))
		req.Header.Set("Authorization", "Bearer wrong")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with the wrong token: %s", path, resp.Status)
		}
	}
}

func TestAHookReachesTheAdapterAndPrintsItsAnswer(t *testing.T) {
	h, _ := newHost(t)
	server := httptest.NewServer(h.Handler(testToken, Path, HookPath))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + Path
	h.Apply(prompt("u1", "clean up", 1))
	h.Apply(assistant("a1", "msg_1", 2, toolUse("toolu_1", "Bash", map[string]any{"command": "rm -rf build"})))
	event, _ := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "session_id": testSession, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "rm -rf build"}})
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- SendHook(context.Background(), endpoint, testToken, bytes.NewReader(event), &out) }()
	eventually(t, "it waits", func() bool { tc := toolCall(t, h, "toolu_1"); return tc != nil && tc["status"] == "pending-confirmation" })
	h.Dispatch(map[string]any{"type": "chat/toolCallConfirmed", "turnId": "turn-u1", "toolCallId": "toolu_1", "approved": true}, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"behavior":"allow"`) {
		t.Errorf("printed %s", out.String())
	}
	// an adapter that is not there: an error, which the hook command reports and swallows
	if err := SendHook(context.Background(), "ws://127.0.0.1:1/ahp", testToken, strings.NewReader(`{"hook_event_name":"Stop"}`), io.Discard); err == nil {
		t.Error("no adapter, no error")
	}
}

func TestTheTailReadsCompleteLinesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var got []string
	tail := &Tail{Path: path, Apply: func(line []byte) { got = append(got, string(line)) }}
	tail.Drain() // not there yet
	f, _ := os.Create(path)
	f.WriteString(`{"a":1}` + "\n" + `{"b":`)
	tail.Drain()
	f.WriteString(`2}` + "\n")
	tail.Drain()
	f.Close()
	if strings.Join(got, "|") != `{"a":1}|{"b":2}` {
		t.Errorf("got %q", got)
	}
	// replaced by a shorter file: read again from its start
	os.WriteFile(path, []byte(`{"c":3}`+"\n"), 0o644)
	tail.Drain()
	if got[len(got)-1] != `{"c":3}` {
		t.Errorf("got %q", got)
	}
	// a path a hook names is taken only while nothing was read
	other := &Tail{Path: filepath.Join(t.TempDir(), "expected.jsonl")}
	other.SetPath(path)
	if other.Path != path {
		t.Error("the transcript Claude Code names wins")
	}
	tail.SetPath("/elsewhere.jsonl")
	if tail.Path != path {
		t.Error("not once it is followed")
	}
}
