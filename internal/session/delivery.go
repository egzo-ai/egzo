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
}

type claimed struct {
	Messages []struct {
		ID   string `json:"id"`
		From string `json:"from"`
		Text string `json:"text"`
	} `json:"messages"`
	Interrupt bool `json:"interrupt"`
}

// Header is the line that tells the harness (and the control sidecar) which message follows.
func Header(id, from string) string { return fmt.Sprintf("[egzo msg %s from %s]", id, from) }

// Run keeps the control sidecar informed and types its messages into the terminal until ctx ends or
// the program exits.
func (h *Holder) RunDelivery(ctx context.Context, api *agentclient.Client, cfg Delivery) {
	if cfg.Interval == 0 {
		cfg.Interval = 500 * time.Millisecond
	}
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
			parts = append(parts, Header(message.ID, message.From)+" "+message.Text)
			outstanding[message.ID] = true
		}
		h.Inject(strings.Join(parts, "\n\n"))
	}
}
