package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/internal/agentclient"
)

// Delivery is how queued messages get into the terminal.
type Delivery struct {
	// HumanQuiet is how long no read-write client may have typed before a message is typed for it.
	HumanQuiet time.Duration
	// AckTimeout is how long the control sidecar waits for the agent to fetch an announced message before it
	// announces it again.
	AckTimeout time.Duration
	// IdleSignal is "hook" (the harness reports its state through hooks) or "quiescence" (quiet
	// output means idle).
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
	// StuckAfter is how long the program's output may stay silent while the control sidecar still says
	// the agent is working (a harness that hooks its turns) before the agent is released: a harness
	// whose hook never reported the end of a turn (an interrupt with Esc, a crash of the hook) would
	// otherwise keep its messages waiting forever. A working TUI draws continuously, so a long silence
	// means it is not working. It defaults to three minutes. Only "working" is released: an agent that
	// is blocked on a dialog stays blocked.
	StuckAfter time.Duration
}

type claimed struct {
	// Line is what to type, composed by the control sidecar from fixed words and message ids.
	Line      string   `json:"line"`
	IDs       []string `json:"ids"`
	Interrupt bool     `json:"interrupt"`
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

func drawn(output []byte, markers []string) bool {
	text := string(output)
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

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
	if cfg.StuckAfter == 0 {
		cfg.StuckAfter = 3 * time.Minute
	}
	ready := false
	var rawSince, released time.Time
	startReported := false

	reported := "starting"
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

		// Announce ourselves until the control sidecar has heard: it may not be reachable yet.
		if !startReported {
			if _, err := api.Do(ctx, "POST", "/v1/activity", map[string]any{"state": "starting", "if_unset": true}); err == nil {
				startReported = true
			}
		}

		if cfg.IdleSignal == "hook" {
			if quiet := h.LastOutput(); time.Since(quiet) >= cfg.StuckAfter && quiet.After(released) {
				if _, err := api.Do(ctx, "POST", "/v1/activity", map[string]string{"state": "idle", "if": "working"}); err == nil {
					released = time.Now()
				}
			}
		}

		if cfg.IdleSignal == "quiescence" {
			wanted := "working"
			if time.Since(h.LastOutput()) >= cfg.Quiescence {
				wanted = "idle"
			}
			if wanted != reported {
				if _, err := api.Do(ctx, "POST", "/v1/activity", map[string]string{"state": wanted}); err == nil {
					reported = wanted
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
		if work.Line == "" {
			continue
		}
		// The line is fixed words and ids from the control sidecar, never message text; Sanitize keeps it
		// that way even if the sidecar were wrong.
		h.Inject(Sanitize(work.Line))
	}
}
