package proxy

import (
	"encoding/json"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

// Event is one line of the audit log. Bodies and query strings are never recorded.
type Event struct {
	Time   time.Time `json:"time"`
	Agent  string    `json:"agent,omitempty"`
	Host   string    `json:"host,omitempty"`
	Action string    `json:"action"` // allow, inject, deny, aborted, request, close, error
	Reason string    `json:"reason,omitempty"`
	Method string    `json:"method,omitempty"`
	Path   string    `json:"path,omitempty"`
	Status int       `json:"status,omitempty"`

	// On a tunnel's "close": what went from the agent to the host and back, and for how long.
	BytesUp    int64 `json:"bytes_up,omitempty"`
	BytesDown  int64 `json:"bytes_down,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// maxField bounds every string an agent can influence: it is logged for each denied attempt, and an
// unbounded value would let one agent fill the log.
const maxField = 256

func clip(text string) string {
	if len(text) <= maxField {
		return text
	}
	cut := maxField
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
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
	event.Agent, event.Host, event.Reason, event.Path = clip(event.Agent), clip(event.Host), clip(event.Reason), clip(event.Path)
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.out.Write(append(line, '\n'))
}
