// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a writer the test can read while the status log writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines() []statusLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []statusLine
	for _, text := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if text == "" {
			continue
		}
		var line statusLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			panic("a status line that is not JSON: " + text)
		}
		out = append(out, line)
	}
	return out
}

func startStatusLog(t *testing.T, interval time.Duration) (*server, *syncBuffer) {
	t.Helper()
	srv, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	l := newStatusLog(srv, out, "demo", interval)
	l.coalesce = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return srv, out
}

func waitForLines(t *testing.T, out *syncBuffer, n int) []statusLine {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lines := out.lines(); len(lines) >= n {
			return lines
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wanted %d status lines, have %d: %+v", n, len(out.lines()), out.lines())
	return nil
}

func TestTheStatusLogStartsWithAStartLineThatNamesTheProjectAndTheInterval(t *testing.T) {
	_, out := startStatusLog(t, time.Hour)
	first := waitForLines(t, out, 1)[0]
	if first.Type != "status" || first.V != 1 || first.Reason != "start" || first.Project != "demo" || first.Interval != 3600 || first.Time.IsZero() {
		t.Errorf("first line = %+v", first)
	}
	if first.Agents == nil {
		t.Error("agents must be a list, empty when there are none: a reader replaces what it knew")
	}
}

func TestAnAgentStateChangeIsALineAndABurstIsOneLine(t *testing.T) {
	srv, out := startStatusLog(t, time.Hour)
	waitForLines(t, out, 1)
	if err := srv.setActivity("coder", "working"); err != nil {
		t.Fatal(err)
	}
	srv.reportStatus("coder", "one")
	srv.reportStatus("coder", "two")
	srv.reportStatus("coder", "three")
	waitForLines(t, out, 2)
	time.Sleep(150 * time.Millisecond) // anything else a burst would have written by now
	lines := out.lines()
	if len(lines) != 2 {
		t.Fatalf("a burst of changes is one line, got %d: %+v", len(lines), lines)
	}
	change := lines[1]
	if change.Reason != "change" || len(change.Agents) != 1 {
		t.Fatalf("change = %+v", change)
	}
	if a := change.Agents[0]; a.Agent != "coder" || a.Activity != "working" || a.Status != "three" {
		t.Errorf("agent = %+v: the line carries the state after the burst", a)
	}
}

func TestAnEventThatChangesNothingAReaderCanSeeIsNotALine(t *testing.T) {
	srv, out := startStatusLog(t, time.Hour)
	waitForLines(t, out, 1)
	srv.setActivity("coder", "idle")
	waitForLines(t, out, 2)
	// a hook that says "idle" again, and a status line repeated, move `updated` and nothing else
	srv.setActivity("coder", "idle")
	srv.events.append(Event{Type: "hook", Agent: "coder", Actor: "agent:coder", Text: "PreToolUse"})
	time.Sleep(200 * time.Millisecond)
	if lines := out.lines(); len(lines) != 2 {
		t.Errorf("lines = %d, want 2: %+v", len(lines), lines)
	}
}

func TestTheHeartbeatRepeatsTheStateWhenNothingChanged(t *testing.T) {
	srv, out := startStatusLog(t, 60*time.Millisecond)
	waitForLines(t, out, 1)
	srv.setActivity("coder", "idle")
	lines := waitForLines(t, out, 4)
	var beats int
	for _, l := range lines {
		if l.Reason == "heartbeat" {
			beats++
			if len(l.Agents) != 1 || l.Agents[0].Activity != "idle" {
				t.Errorf("a heartbeat carries the whole state: %+v", l)
			}
		}
	}
	if beats < 2 {
		t.Errorf("heartbeats = %d in %+v", beats, lines)
	}
}

func TestRegisteringAnAgentIsALineBeforeItHasSaidAnything(t *testing.T) {
	srv, out := startStatusLog(t, time.Hour)
	waitForLines(t, out, 1)
	rec := newRecorder()
	req := mustRequest("PUT", "/agents/coder-1", nil)
	req.SetPathValue("name", "coder-1")
	srv.registerAgent(rec, req)
	if rec.Code != 204 {
		t.Fatalf("register = %d", rec.Code)
	}
	lines := waitForLines(t, out, 2)
	if got := lines[1]; got.Reason != "change" || len(got.Agents) != 1 || got.Agents[0].Agent != "coder-1" {
		t.Errorf("line = %+v", got)
	}
}

func TestAnUnusableIntervalIsTheDefaultAndSaysSoOnStandardError(t *testing.T) {
	t.Setenv(EnvStatusInterval, "soon")
	var warned bytes.Buffer
	saved := logger
	logger = newLogger(&warned)
	t.Cleanup(func() { logger = saved })
	if got := statusInterval(); got != defaultStatusInterval {
		t.Errorf("interval = %v", got)
	}
	var entry map[string]any
	if err := json.Unmarshal(warned.Bytes(), &entry); err != nil || entry["level"] != "warn" || entry["msg"] == "" {
		t.Errorf("stderr = %q (%v): want one JSON object with a lowercase level", warned.String(), err)
	}
	t.Setenv(EnvStatusInterval, "5")
	if got := statusInterval(); got != 5*time.Second {
		t.Errorf("interval = %v", got)
	}
}
