// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The control sidecar's logs are JSON, one object per line, split in two:
//
//   - standard output carries the agents' status, and nothing else: a status line says what every agent of the project
//     is doing, written when something changes and again every interval. A client (the hub) follows the container's
//     log through the engine's own API and keeps the latest line, so it never has to run a command in the sidecar to
//     ask. The engines tell the two streams apart, whatever log driver the sysadmin chose (see
//     known-issues/container-logging-follows-the-engine-default.md).
//   - standard error carries everything else control says: warnings and errors, as {"time","level","msg",...}.
//
// A status line is {"type":"status","v":1,"time","project","reason","interval","agents":[...]}. reason is `start` (the
// sidecar came up), `change` (an agent's state changed) or `heartbeat` (nothing did; this is the state, still). interval is
// how often a heartbeat is written, in seconds, so a reader knows how old a line may be before the sidecar is suspect.
const (
	statusLogVersion = 1
	// defaultStatusInterval is the heartbeat. It is a trade: the log grows by one line per interval, the reader is
	// that stale at worst when it joins an idle sidecar and has to wait for a line.
	defaultStatusInterval = 30 * time.Second
	// statusCoalesce is how long a change waits for more, so a burst of hooks is one line.
	statusCoalesce = 300 * time.Millisecond
	// EnvStatusInterval sets the heartbeat, in seconds, for tests and for a sysadmin who wants it tighter or looser.
	EnvStatusInterval = "EGZO_STATUS_INTERVAL"
)

// logger is where control reports what is wrong: JSON on standard error.
var logger = newLogger(os.Stderr)

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.LevelKey {
				return slog.String(slog.LevelKey, strings.ToLower(a.Value.String()))
			}
			return a
		},
	}))
}

type statusLine struct {
	Type     string        `json:"type"`
	V        int           `json:"v"`
	Time     time.Time     `json:"time"`
	Project  string        `json:"project"`
	Reason   string        `json:"reason"`
	Interval int           `json:"interval"`
	Agents   []AgentStatus `json:"agents"`
}

// statusLog writes the status lines.
type statusLog struct {
	server   *server
	project  string
	interval time.Duration
	coalesce time.Duration

	mu   sync.Mutex // one writer at a time, and the last state written
	out  io.Writer
	last []AgentStatus
}

// statusInterval reads the heartbeat from the environment; anything unusable is the default.
func statusInterval() time.Duration {
	if v := os.Getenv(EnvStatusInterval); v != "" {
		if seconds, err := strconv.Atoi(v); err == nil && seconds >= 1 {
			return time.Duration(seconds) * time.Second
		}
		logger.Warn("ignoring an unusable status interval", "env", EnvStatusInterval, "value", v)
	}
	return defaultStatusInterval
}

func newStatusLog(s *server, out io.Writer, project string, interval time.Duration) *statusLog {
	return &statusLog{server: s, out: out, project: project, interval: interval, coalesce: statusCoalesce}
}

// sameState compares what matters to a reader. `updated` moves with every event of an agent, including the hooks that
// change nothing it can see, so it is left out: the line is for changes of state.
func sameState(a, b []AgentStatus) bool {
	strip := func(in []AgentStatus) []AgentStatus {
		out := make([]AgentStatus, len(in))
		for i, s := range in {
			s.Updated = time.Time{}
			out[i] = s
		}
		return out
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

// write logs the state of the agents with a reason. A `change` that changed nothing is not written.
func (l *statusLog) write(reason string) {
	agents := l.server.agentStatuses()
	l.mu.Lock()
	defer l.mu.Unlock()
	if reason == "change" && sameState(l.last, agents) {
		return
	}
	l.last = agents
	line := statusLine{Type: "status", V: statusLogVersion, Time: time.Now().UTC(), Project: l.project, Reason: reason,
		Interval: int(l.interval / time.Second), Agents: agents}
	data, err := json.Marshal(line)
	if err != nil {
		logger.Error("cannot write a status line", "err", err)
		return
	}
	if _, err := l.out.Write(append(data, '\n')); err != nil {
		logger.Error("cannot write a status line", "err", err)
	}
}

// run writes the first line, then a line for every change and a heartbeat every interval, until ctx ends.
func (l *statusLog) run(ctx context.Context) {
	l.write("start")
	beat := time.NewTicker(l.interval)
	defer beat.Stop()
	for {
		wake := l.server.events.waker()
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			l.write("heartbeat")
		case <-wake:
		case <-l.server.registry:
		}
		if ctx.Err() != nil {
			return
		}
		// Whatever woke us, wait a moment for the rest of the burst, then write what the state is now.
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.coalesce):
		}
		l.write("change")
	}
}
