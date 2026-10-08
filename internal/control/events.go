// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Event is one entry of the typed event stream. Every client, whether the CLI, the hub or a chat
// integration, reads the same stream, and every entry says who acted.
type Event struct {
	Seq   int             `json:"seq"`
	Time  time.Time       `json:"time"`
	Type  string          `json:"type"` // status, message, announced, fetched, resolved, unconfirmed, interrupt, interrupted, activity, hook
	Agent string          `json:"agent,omitempty"`
	Actor string          `json:"actor,omitempty"` // agent:<name> or user:<id>; operator for the CLI
	ID    string          `json:"id,omitempty"`    // message id
	Text  string          `json:"text,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// store is an append-only log on the control volume, with the live view derived from it: the messages
// and their states are kept in memory, rebuilt from the log when the sidecar starts, and indexed so
// that what a request costs does not grow with the history.
type store struct {
	mu          sync.Mutex
	path        string
	file        *os.File // the log, open for appending
	size        int64
	maxLogBytes int64 // the log is compacted past this size
	skipped     int   // lines of the log that could not be read when it was opened
	events      []Event
	lastSeq     int
	wake        chan struct{} // closed and replaced whenever an event is appended

	messages  map[string]*Message
	order     []string
	byAddress map[string][]string // address -> ids of the messages it sent or received
	announced map[string]bool     // ids of the messages announced and not yet fetched
	latest    map[string]AgentStatus
	pending   map[string]bool // agents with an interrupt requested and not yet handed over
	open      map[string]int  // agent -> requests and questions it fetched and has not resolved
	waiting   map[string]int  // agent -> questions it asked that nobody has answered
}

// defaultMaxLogBytes is when the log is compacted: hook events, which are the bulk of it, and superseded
// status and activity lines are dropped; messages and everything that defines their state are kept.
const defaultMaxLogBytes = 64 << 20

// maxLogLine is the longest line read back; a longer one is skipped, never fatal.
const maxLogLine = 8 << 20

func newStore(path string) *store {
	return &store{
		path: path, wake: make(chan struct{}), maxLogBytes: defaultMaxLogBytes,
		messages: map[string]*Message{}, byAddress: map[string][]string{}, announced: map[string]bool{},
		latest: map[string]AgentStatus{}, pending: map[string]bool{}, open: map[string]int{}, waiting: map[string]int{},
	}
}

func openStore(dir string) (*store, error) {
	s := newStore(filepath.Join(dir, "events.jsonl"))
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	endsWithNewline := true
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			s.size += int64(len(line))
			endsWithNewline = line[len(line)-1] == '\n'
			var event Event
			if len(line) > maxLogLine || json.Unmarshal(line, &event) != nil {
				s.skipped++
			} else {
				if event.Seq > s.lastSeq {
					s.lastSeq = event.Seq
				}
				s.events = append(s.events, event)
				s.index(event)
			}
		}
		if err != nil {
			if err != io.EOF {
				return nil, err
			}
			break
		}
	}
	if s.skipped > 0 {
		fmt.Fprintf(os.Stderr, "control: skipped %d unreadable line(s) of the event log\n", s.skipped)
	}
	if !endsWithNewline {
		// A crash left half a line: end it, or the next event would be glued to it and lost.
		if err := s.write([]byte("\n")); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *store) write(data []byte) error {
	if s.file == nil {
		file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.file = file
	}
	n, err := s.file.Write(data)
	s.size += int64(n)
	return err
}

func (s *store) append(event Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Seq = s.lastSeq + 1
	event.Time = time.Now().UTC()
	line, err := json.Marshal(event)
	if err != nil {
		return event, err
	}
	if err := s.write(append(line, '\n')); err != nil {
		return event, err
	}
	s.add(event)
	if s.maxLogBytes > 0 && s.size > s.maxLogBytes {
		if err := s.compact(); err != nil {
			fmt.Fprintf(os.Stderr, "control: compacting the event log: %v\n", err)
		}
	}
	return event, nil
}

// add folds an event into memory and wakes the followers. It runs under the lock.
func (s *store) add(event Event) {
	s.lastSeq = event.Seq
	s.events = append(s.events, event)
	s.index(event)
	close(s.wake)
	s.wake = make(chan struct{})
}

// compact rewrites the log without what nothing needs any more. It runs under the lock.
func (s *store) compact() error {
	lastOf := map[string]int{} // agent and type -> index of the newest such event
	for i, event := range s.events {
		switch event.Type {
		case "status", "activity":
			lastOf[event.Agent+"\x00"+event.Type] = i
		case "interrupt", "interrupted":
			lastOf[event.Agent+"\x00interrupt"] = i
		}
	}
	var kept []Event
	for i, event := range s.events {
		switch event.Type {
		case "hook":
			continue
		case "status", "activity":
			if lastOf[event.Agent+"\x00"+event.Type] != i {
				continue
			}
		case "interrupt", "interrupted":
			if lastOf[event.Agent+"\x00interrupt"] != i {
				continue
			}
		}
		kept = append(kept, event)
	}
	var out bytes.Buffer
	for _, event := range kept {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, out.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temp, s.path); err != nil {
		return err
	}
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
	s.events, s.size = kept, int64(out.Len())
	return nil
}

// since returns the events after seq, filtered by agent when one is given, and a channel that
// closes when a newer event arrives.
func (s *store) since(seq int, agent string) ([]Event, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := sort.Search(len(s.events), func(i int) bool { return s.events[i].Seq > seq })
	var out []Event
	for _, event := range s.events[start:] {
		if agent == "" || event.Agent == agent {
			out = append(out, event)
		}
	}
	return out, s.wake
}

// all returns the whole log without copying it: the log only ever grows (or is replaced as a whole),
// so what was returned stays valid.
func (s *store) all() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[:len(s.events):len(s.events)]
}

// activity is what an agent is doing: starting, idle, working or blocked. It is the last activity event.
func (s *store) activity(agent string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest[agent].Activity
}

// interruptPending reports whether an interrupt was requested for an agent and not yet handed over.
func (s *store) interruptPending(agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending[agent]
}

// AgentStatus is what the control sidecar knows of an agent: the status line it set, its activity, and what
// it owes (open requests, an unanswered question).
type AgentStatus struct {
	Agent    string    `json:"agent"`
	Status   string    `json:"status"`
	Activity string    `json:"activity"`
	Open     int       `json:"open"`
	Waiting  bool      `json:"waiting"`
	Updated  time.Time `json:"updated"`
}

func (s *store) statuses() map[string]AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AgentStatus, len(s.latest))
	for name, status := range s.latest {
		out[name] = status
	}
	return out
}
