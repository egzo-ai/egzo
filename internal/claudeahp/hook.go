// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package claudeahp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The adapter's paths, under the address of the AHP server egzo describes (harness.AHPEndpoint).
const (
	Path     = "/ahp"
	HookPath = "/hook"
)

// hookURL is the hooks' address next to the AHP server's.
func hookURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("%s is not a WebSocket address", endpoint)
	}
	u.Path = HookPath
	return u.String(), nil
}

// quickHook bounds a hook other than PermissionRequest: it must never hold Claude Code up for long.
const quickHook = 10 * time.Second

// SendHook passes one hook event (Claude Code's JSON on in) to the adapter at endpoint, and writes its answer to out. A PermissionRequest
// waits for its answer for as long as Claude Code lets the hook run; the rest are quick.
func SendHook(ctx context.Context, endpoint, token string, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(in, 4<<20))
	if err != nil {
		return err
	}
	var event HookEvent
	if json.Unmarshal(body, &event) != nil {
		return fmt.Errorf("the hook's input is not a JSON object")
	}
	if event.Event != "PermissionRequest" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, quickHook)
		defer cancel()
	}
	target, err := hookURL(endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the adapter answered %s", resp.Status)
	}
	_, err = io.Copy(out, io.LimitReader(resp.Body, 4<<20))
	return err
}
