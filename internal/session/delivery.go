package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/internal/agentclient"
)

// Delivery is how queued messages get into the terminal.
type Delivery struct {
	// HumanQuiet is how long no read-write client may have typed before a message is typed for it.
	HumanQuiet time.Duration
	// AckTimeout is how long the control sidecar waits for the harness to acknowledge a message.
	AckTimeout time.Duration
	// IdleSignal is "hook" (the harness reports its state through hooks) or "quiescence" (quiet
	// output means idle, and the echoed header is the acknowledgement).
	IdleSignal string
	// Quiescence is how long the output stays quiet before the agent counts as idle.
	Quiescence time.Duration
	// InterruptKey is what the harness takes as "stop what you are doing".
	InterruptKey string
	// Interval is how often the loop looks; it defaults to half a second.
	Interval time.Duration
	// Settle is how long the program's output must have been quiet, once since it started, before
	// anything is typed into it. It defaults to a second.
	Settle time.Duration
	// RequireRaw holds everything back until the program has taken the terminal over. A TUI announces
	// itself (a hook, a plugin loading) before it does, and the terminal discards what was typed
	// before the change, so a message typed too early is silently lost, and a boot has quiet gaps
	// that look like readiness (a TUI waits for a terminal that answers its queries).
	RequireRaw bool
	// ReadyMarkers are texts the TUI draws once it takes input (its prompt box); any one of them
	// appearing in the output means it is ready. A TUI takes the terminal over and then spends a few
	// seconds starting before it reads, so the terminal alone is not enough. When none appears within
	// ReadyTimeout of the takeover, delivery goes ahead anyway: a harness whose screen changed must
	// not strand its messages.
	ReadyMarkers []string
	ReadyTimeout time.Duration
}

type claimed struct {
	Messages []struct {
		ID   string `json:"id"`
		From string `json:"from"`
		Text string `json:"text"`
	} `json:"messages"`
	Interrupt bool `json:"interrupt"`
}

func drawn(output []byte, markers []string) bool {
	text := string(output)
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Sanitize removes what could leave a bracketed paste: the escape character and the other control
// characters a terminal acts on (newline and tab stay). A message can come from another agent, and
// text that ends the paste early would be typed into the TUI as keystrokes, answering its dialogs.
func Sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, text)
}

// Header is the line that tells the harness (and the control sidecar) which message follows.
func Header(id, from string) string { return fmt.Sprintf("[egzo msg %s from %s]", id, from) }

// Run keeps the control sidecar informed and types its messages into the terminal until ctx ends or
// the program exits.
func (h *Holder) RunDelivery(ctx context.Context, api *agentclient.Client, cfg Delivery) {
	if cfg.Interval == 0 {
		cfg.Interval = 500 * time.Millisecond
	}
	if cfg.Settle == 0 {
		cfg.Settle = time.Second
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = time.Minute
	}
	ready := false
	var rawSince time.Time
	api.Do(ctx, "POST", "/v1/activity", map[string]string{"state": "starting"})

	reported := "starting"
	outstanding := map[string]bool{} // typed, not yet seen echoed (quiescence harnesses acknowledge by echo)
	var lastClaim time.Time
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.Done():
			return
		case <-ticker.C:
		}

		if cfg.IdleSignal == "quiescence" {
			wanted := "busy"
			if time.Since(h.LastOutput()) >= cfg.Quiescence {
				wanted = "idle"
			}
			if wanted != reported {
				if _, err := api.Do(ctx, "POST", "/v1/activity", map[string]string{"state": wanted}); err == nil {
					reported = wanted
				}
			}
			if len(outstanding) > 0 {
				output := string(h.Output())
				var seen []string
				for id := range outstanding {
					if strings.Contains(output, "[egzo msg "+id) {
						seen = append(seen, id)
					}
				}
				if len(seen) > 0 {
					if _, err := api.Do(ctx, "POST", "/v1/ack", map[string]any{"ids": seen}); err == nil {
						for _, id := range seen {
							delete(outstanding, id)
						}
					}
				}
			}
		}

		if !ready {
			switch {
			case cfg.RequireRaw:
				// the TUI has the terminal; a moment more for it to start reading
				if !h.Raw() {
					rawSince = time.Time{}
					continue
				}
				if rawSince.IsZero() {
					rawSince = time.Now()
				}
				if time.Since(rawSince) < 300*time.Millisecond {
					continue
				}
				if len(cfg.ReadyMarkers) > 0 && time.Since(rawSince) < cfg.ReadyTimeout && !drawn(h.Output(), cfg.ReadyMarkers) {
					continue
				}
				ready = true
			case h.Wrote() && time.Since(h.LastOutput()) >= cfg.Settle:
				ready = true
			default:
				continue
			}
		}
		if time.Since(h.LastHumanInput()) < cfg.HumanQuiet || time.Since(lastClaim) < time.Second {
			continue
		}
		lastClaim = time.Now()
		reply, err := api.Do(ctx, "POST", "/v1/claim", map[string]any{"ack_timeout_ms": cfg.AckTimeout.Milliseconds()})
		if err != nil {
			continue
		}
		var work claimed
		if json.Unmarshal(reply, &work) != nil {
			continue
		}
		if work.Interrupt && cfg.InterruptKey != "" {
			h.Send(cfg.InterruptKey)
		}
		if len(work.Messages) == 0 {
			continue
		}
		parts := make([]string, 0, len(work.Messages))
		for _, message := range work.Messages {
			parts = append(parts, Header(message.ID, Sanitize(message.From))+" "+Sanitize(message.Text))
			outstanding[message.ID] = true
		}
		h.Inject(strings.Join(parts, "\n\n"))
	}
}
