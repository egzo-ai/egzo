// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// promptClientID is who starts the turns of egzo's lines (the task, the announcements of messages) on the AHP server.
const promptClientID = "egzo-prompt"

// Prompt starts a turn of the main session's chat with text, as a person's prompt, through the harness's AHP server, and waits for
// the server to take it. It waits for the main session (egzo names it before the harness makes it) until ctx ends.
//
// A turn the AHP server starts is its own from the first event: a prompt that reached the harness another way, while a client was
// first listing the session, used to be lost from the chat (the server loads the turns that began before it learned of a session,
// and follows the ones after, and a turn under way fell between).
func Prompt(ctx context.Context, url, token, mainFile, text string) error {
	conn, _, err := dial(ctx, url, token)
	if err != nil {
		return fmt.Errorf("reach the AHP server: %w", err)
	}
	conn.SetReadLimit(MaxMessage)
	defer conn.CloseNow()
	next := 0
	send := func(m map[string]any) error {
		data, _ := json.Marshal(m)
		return conn.Write(ctx, websocket.MessageText, data)
	}
	// ask sends a request and waits for its answer, skipping what else the server says
	ask := func(method string, params any) (json.RawMessage, error) {
		next++
		if err := send(map[string]any{"jsonrpc": "2.0", "id": next, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &m) != nil || m.ID == nil || *m.ID != next {
				continue
			}
			if m.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
			}
			return m.Result, nil
		}
	}
	if _, err := ask("initialize", map[string]any{"channel": RootChannel, "clientId": promptClientID, "protocolVersions": []string{"1.0.0"},
		"initialSubscriptions": []string{}}); err != nil {
		return err
	}
	// the server knows a session it did not make once it lists it
	var main MainSession
	for {
		if id := readMain(mainFile); id != "" {
			result, err := ask("listSessions", map[string]any{})
			if err != nil {
				return err
			}
			if listed(result, SessionURIs(id).Session) {
				main = SessionURIs(id)
				break
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("the main session never appeared")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if _, err := ask("subscribe", map[string]any{"channel": main.Chat}); err != nil {
		return err
	}
	// dispatch sends an action of the main chat and waits for the server's envelope of it, which has a reason when it was turned down
	seq := 0
	dispatch := func(action map[string]any, key string) (string, error) {
		seq++
		if err := send(map[string]any{"jsonrpc": "2.0", "method": "dispatchAction", "params": map[string]any{"channel": main.Chat, "clientSeq": seq,
			"action": action}}); err != nil {
			return "", err
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return "", fmt.Errorf("%s was not confirmed: %w", action["type"], err)
			}
			var m struct {
				Method string `json:"method"`
				Params struct {
					Action          map[string]any `json:"action"`
					RejectionReason string         `json:"rejectionReason"`
				} `json:"params"`
			}
			if json.Unmarshal(data, &m) != nil || m.Method != "action" || m.Params.Action["type"] != action["type"] || m.Params.Action[key] != action[key] {
				continue
			}
			return m.Params.RejectionReason, nil
		}
	}
	message := map[string]any{"text": text, "origin": map[string]any{"kind": "user"}}
	refused, err := dispatch(map[string]any{"type": "chat/turnStarted", "turnId": newID(), "message": message}, "turnId")
	if err != nil || refused == "" {
		return err
	}
	// a turn under way (someone prompted in the meantime): the line waits for its end, queued, rather than being lost
	queued, err := dispatch(map[string]any{"type": "chat/pendingMessageSet", "kind": "queued", "id": newID(), "message": message}, "id")
	if err != nil {
		return err
	}
	if queued != "" {
		return fmt.Errorf("the turn was turned down (%s), and so was queueing it (%s)", refused, queued)
	}
	return nil
}

// newID is a random UUID, the form the hub's ids have.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
