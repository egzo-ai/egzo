// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package agentclient is the agent's side of the control sidecar's agent API: what runs inside an
// agent container (the session holder, the hook command) uses it with the agent's own credentials.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client calls the control sidecar as the agent whose credentials the environment holds.
type Client struct {
	URL, Agent, Token string
	HTTP              *http.Client
}

// FromEnv builds a client from EGZO_CONTROL_URL, EGZO_AGENT and EGZO_TOKEN.
func FromEnv() (*Client, error) {
	c := &Client{URL: os.Getenv("EGZO_CONTROL_URL"), Agent: os.Getenv("EGZO_AGENT"), Token: os.Getenv("EGZO_TOKEN")}
	if c.URL == "" || c.Agent == "" || c.Token == "" {
		return nil, fmt.Errorf("EGZO_CONTROL_URL, EGZO_AGENT and EGZO_TOKEN must be set (is this an egzo agent container?)")
	}
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c, nil
}

// StatusError is a refusal by the control sidecar.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// Temporary reports whether trying again could help: the sidecar was busy or not there yet, as opposed
// to refusing the request itself.
func Temporary(err error) bool {
	var refused *StatusError
	if errors.As(err, &refused) {
		return refused.Code == http.StatusTooManyRequests || refused.Code >= 500
	}
	return err != nil
}

// Do sends one request and returns the response body. A non-2xx status is an error.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.Agent, c.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReply+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReply {
		return nil, fmt.Errorf("control's reply is too large (over %d bytes)", maxReply)
	}
	if response.StatusCode/100 != 2 {
		return data, &StatusError{Code: response.StatusCode, Message: fmt.Sprintf("control answered %s: %s", response.Status, strings.TrimSpace(string(data)))}
	}
	return data, nil
}

// maxReply bounds what is read from control; replies are small by design.
const maxReply = 1 << 20
