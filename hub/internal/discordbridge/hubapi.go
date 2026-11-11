// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discordbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The hub's bridge API (docs/hub-bridges.md), as this bridge uses it. Its types are the API's JSON, kept here: the bridge does not import
// the hub.

// Event is one entry of the hub's outbox: a message between an agent and a user, or a change.
type Event struct {
	Seq     int64    `json:"seq"`
	Type    string   `json:"type"`
	Project string   `json:"project"`
	Agent   string   `json:"agent"`
	ID      string   `json:"id"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Kind    string   `json:"kind"`
	Re      string   `json:"re"`
	Text    string   `json:"text"`
	Choices []string `json:"choices"`
	Outcome string   `json:"outcome"`
	User    string   `json:"user"`
	Bridge  string   `json:"bridge"`
	Account string   `json:"account"`
}

// Inbound is what a person said, for the hub to check and deliver.
type Inbound struct {
	Project   string `json:"project"`
	Agent     string `json:"agent"`
	Account   string `json:"account"`
	Text      string `json:"text"`
	Key       string `json:"key,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
}

// Account is a Discord account linked to a hub user.
type Account struct {
	Account  string `json:"account"`
	Name     string `json:"name"`
	User     string `json:"user"`
	Username string `json:"username"`
}

// ChatAgent is an agent a person may chat with.
type ChatAgent struct {
	Project     string `json:"project"`
	ProjectName string `json:"project_name"`
	Agent       string `json:"agent"`
	Template    string `json:"template"`
	State       string `json:"state"`
	Admin       bool   `json:"admin"`
}

// ErrNotLinked is a person whose Discord account is linked to no hub user.
var ErrNotLinked = errors.New("this Discord account is not linked to the hub")

// ErrNotDelivered is a message the hub refused, or a project that is offline. The hub never says which to a bridge.
type ErrNotDelivered struct{ Offline bool }

func (e ErrNotDelivered) Error() string {
	if e.Offline {
		return "not delivered: the project is offline"
	}
	return "not delivered"
}

// HubAPI is the hub's bridge API.
type HubAPI struct {
	URL    string // the hub, such as http://hub:8080
	Secret string
	HTTP   *http.Client
}

// hubTimeout bounds a call to the hub, the outbox's stream aside: each takes milliseconds, and a hub that hangs must not hold the bridge.
const hubTimeout = 10 * time.Second

func (h *HubAPI) do(ctx context.Context, method, path string, body, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, hubTimeout)
	defer cancel()
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(h.URL, "/")+"/bridge/v1"+path, payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := h.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, errors.New("the hub refused the bridge's secret: check EGZO_BRIDGE_SECRET against the hub's EGZO_BRIDGES")
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// Deliver hands a person's message to the hub, and returns the id the agent gets.
func (h *HubAPI) Deliver(ctx context.Context, in Inbound) (string, error) {
	var out struct{ ID string }
	status, err := h.do(ctx, http.MethodPost, "/messages", in, &out)
	switch {
	case err != nil:
		return "", err
	case status == http.StatusServiceUnavailable:
		return "", ErrNotDelivered{Offline: true}
	case status != http.StatusOK:
		return "", ErrNotDelivered{}
	}
	return out.ID, nil
}

// Why a message the hub gave the bridge did not reach the person: the hub's codes, the same for every bridge (docs/hub-bridges.md).
const (
	UndeliveredUnreachable = "person-unreachable" // Discord will not let the bot reach the person: DMs off, no shared server
	UndeliveredNoPlace     = "no-place"           // nowhere to put it
	UndeliveredRefused     = "refused"            // Discord refused it for good
	UndeliveredUnavailable = "unavailable"        // Discord did not take it in RetryFor
)

// Undelivered tells the hub that a message it gave the bridge, for account, did not reach the person. detail is what Discord said.
func (h *HubAPI) Undelivered(ctx context.Context, message, account, code, detail string) error {
	status, err := h.do(ctx, http.MethodPost, "/messages/"+url.PathEscape(message)+"/undelivered", map[string]string{"account": account, "code": code, "detail": detail}, nil)
	switch {
	case err != nil:
		return err
	case status != http.StatusNoContent:
		return fmt.Errorf("the hub answered %d", status)
	}
	return nil
}

// Code makes a link code for a Discord account, and the address the person opens with it.
func (h *HubAPI) Code(ctx context.Context, account, name string) (code, url string, expires time.Time, err error) {
	var out struct {
		Code, URL string
		Expires   time.Time
	}
	status, err := h.do(ctx, http.MethodPost, "/codes", map[string]string{"account": account, "name": name}, &out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the hub answered %d", status)
	}
	return out.Code, out.URL, out.Expires, err
}

// Accounts lists the Discord accounts linked to hub users.
func (h *HubAPI) Accounts(ctx context.Context) ([]Account, error) {
	var out []Account
	status, err := h.do(ctx, http.MethodGet, "/accounts", nil, &out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the hub answered %d", status)
	}
	return out, err
}

// Agents lists the agents a person may chat with.
func (h *HubAPI) Agents(ctx context.Context, account string) ([]ChatAgent, error) {
	var out []ChatAgent
	status, err := h.do(ctx, http.MethodGet, "/agents?account="+account, nil, &out)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusForbidden:
		return nil, ErrNotLinked
	case status != http.StatusOK:
		return nil, fmt.Errorf("the hub answered %d", status)
	}
	return out, nil
}

// MayAdd says whether a person may add an agent to a place.
func (h *HubAPI) MayAdd(ctx context.Context, account, project, agent string) (bool, error) {
	var out struct{ Allowed bool }
	status, err := h.do(ctx, http.MethodPost, "/may-add", map[string]string{"account": account, "project": project, "agent": agent}, &out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the hub answered %d", status)
	}
	return out.Allowed, err
}

// Stream reads the outbox after seq and hands on each event, until ctx ends or the stream does.
func (h *HubAPI) Stream(ctx context.Context, after int64, on func(Event) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(h.URL, "/")+"/bridge/v1/messages?after="+strconv.FormatInt(after, 10), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Secret)
	resp, err := (&http.Client{}).Do(req) // no timeout: the stream stays open
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the hub's stream answered %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue // a keepalive
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if err := on(e); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("the hub's stream ended")
}

// What the hub says of an agent the bridge keeps a role for.
const (
	Present = "present"
	Absent  = "absent"
	Unknown = "unknown" // the hub has not heard from the project since it started
)

// AgentRef names an agent of a project.
type AgentRef struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
}

// AgentPresence is whether an agent is still in its project: Present, Absent or Unknown.
type AgentPresence struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
	State   string `json:"state"`
}

// Presence asks the hub whether agents are still in their projects, at most 1000 at a time.
func (h *HubAPI) Presence(ctx context.Context, agents []AgentRef) ([]AgentPresence, error) {
	var out struct{ Agents []AgentPresence }
	status, err := h.do(ctx, http.MethodPost, "/presence", map[string]any{"agents": agents}, &out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the hub answered %d", status)
	}
	return out.Agents, err
}
