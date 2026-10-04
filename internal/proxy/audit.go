package proxy

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Event is one line of the audit log. Bodies and query strings are never recorded.
type Event struct {
	Time   time.Time `json:"time"`
	Agent  string    `json:"agent,omitempty"`
	Host   string    `json:"host,omitempty"`
	Action string    `json:"action"` // allow, inject, deny, aborted, request, error
	Reason string    `json:"reason,omitempty"`
	Method string    `json:"method,omitempty"`
	Path   string    `json:"path,omitempty"`
	Status int       `json:"status,omitempty"`
}

// Audit writes events as JSON lines.
type Audit struct {
	mu  sync.Mutex
	out io.Writer
}

func NewAudit(out io.Writer) *Audit { return &Audit{out: out} }

func (a *Audit) Log(event Event) {
	if a == nil || a.out == nil {
		return
	}
	event.Time = time.Now().UTC()
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.out.Write(append(line, '\n'))
}
