// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is where Discord's API is.
const DefaultURL = "https://discord.com/api/v10"

const (
	// globalPerSecond is Discord's global limit on requests with a bot token.
	globalPerSecond = 50
	// invalidLimit is how many invalid requests (401, 403, 429) the client allows itself in invalidWindow. Discord bans the address for
	// a while at 10,000: the client stops well before.
	invalidLimit  = 5000
	invalidWindow = 10 * time.Minute
	// retries is how often a request limited with 429 is tried again.
	retries = 3
	// maxResponse is the longest answer read.
	maxResponse = 8 << 20
)

// Error is what Discord answered to a request that failed.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("discord: %d %s (code %d)", e.Status, e.Message, e.Code)
}

// ErrCodeCannotMessageUser is Discord's code for a DM the person does not take: they turned off DMs from server members, or share no
// server with the bot.
const ErrCodeCannotMessageUser = 50007

// ErrTooManyInvalid is returned instead of sending a request once the client has made too many invalid ones: Discord would ban it.
var ErrTooManyInvalid = errors.New("discord: too many invalid requests in the last 10 minutes, holding back")

// REST is Discord's HTTP API, with its rate limits.
type REST struct {
	token string
	base  string
	http  *http.Client
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	invalidLimit int // invalidLimit, lower in tests

	mu      sync.Mutex
	buckets map[string]*bucket // by route, or by Discord's bucket and the major parameters once Discord named it
	hashes  map[string]string  // route template -> the bucket Discord says it shares (X-RateLimit-Bucket)
	global  time.Time          // no request before this: a global 429
	second  time.Time          // the second the global count is for
	count   int
	invalid []time.Time
}

// bucket is what Discord said of one route's limit: how many requests are left until when.
type bucket struct {
	mu        sync.Mutex
	remaining int
	reset     time.Time
	known     bool
}

// NewREST is the API at base (DefaultURL, or a fake's) with a bot token.
func NewREST(token, base string, client *http.Client) *REST {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &REST{token: token, base: strings.TrimSuffix(base, "/"), http: client, now: time.Now, sleep: sleep, buckets: map[string]*bucket{},
		hashes: map[string]string{}, invalidLimit: invalidLimit}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limitKeys are a request's route, its template (the route with its major parameters left out) and the major parameters.
func limitKeys(method, path string) (key, template, majors string) {
	key = route(method, path)
	if !strings.Contains(key, "/") { // deleting a message: a limit of its own per channel, never shared
		return key, key, key
	}
	var ids, parts []string
	for _, p := range strings.Split(key, "/") {
		if i := strings.LastIndex(p, " "); i >= 0 { // "POST channels"
			parts = append(parts, p)
			continue
		}
		if isID(p) {
			ids = append(ids, p)
			p = "{major}"
		}
		parts = append(parts, p)
	}
	return key, strings.Join(parts, "/"), strings.Join(ids, "/")
}

// bucketKey is where a request's limit is kept: the bucket Discord named for its route with its major parameters, else its route.
func (c *REST) bucketKey(method, path string) string {
	key, template, majors := limitKeys(method, path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if hash := c.hashes[template]; hash != "" {
		return hash + ":" + majors
	}
	return key
}

// route is the rate limit's key for a request: the method and the path, with the ids that are not a major parameter (a channel, a guild,
// a webhook) left out. Discord limits each route per major parameter.
func route(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if i > 0 && isID(p) {
			switch parts[i-1] {
			case "channels", "guilds", "webhooks":
				continue
			}
			parts[i] = "{id}"
		}
		if i > 1 && (parts[i-2] == "interactions" || parts[i-2] == "webhooks") && !isID(p) {
			parts[i] = "{token}" // an interaction's or a webhook's token is a secret, and never in a key
		}
	}
	if method == http.MethodDelete && len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" {
		return "DELETE message " + parts[1] // deleting messages has a limit of its own
	}
	return method + " " + strings.Join(parts, "/")
}

func isID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Do sends a request and decodes the answer into out (nil: none). body is JSON, or a *multipartBody. It waits as Discord's limits say,
// and tries again after a 429.
func (c *REST) Do(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out, "")
}

// do is Do with a reason for the guild's audit log.
func (c *REST) do(ctx context.Context, method, path string, body, out any, reason string) error {
	var payload []byte
	contentType := ""
	switch b := body.(type) {
	case nil:
	case *multipartBody:
		payload, contentType = b.data, b.contentType
	default:
		data, err := marshal(b)
		if err != nil {
			return err
		}
		payload, contentType = data, "application/json"
	}
	for attempt := 0; ; attempt++ {
		key := c.bucketKey(method, path)
		if err := c.wait(ctx, key); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(path, "/interactions/") { // answering an interaction takes its token, not the bot's
			req.Header.Set("Authorization", "Bot "+c.token)
		}
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/egzo-ai/egzo, 1)")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if reason != "" {
			req.Header.Set("X-Audit-Log-Reason", url.PathEscape(reason))
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if method == http.MethodGet && attempt < retries && ctx.Err() == nil {
				if err := c.sleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		resp.Body.Close()
		if err != nil {
			return err
		}
		c.learn(method, path, key, resp.Header)
		switch {
		case retryable(method, resp.StatusCode) && attempt < retries:
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		case resp.StatusCode == http.StatusTooManyRequests:
			c.noteInvalid(resp.Header.Get("X-RateLimit-Scope") != "shared")
			wait := retryAfter(resp.Header, data)
			if resp.Header.Get("X-RateLimit-Global") == "true" {
				c.mu.Lock()
				c.global = c.now().Add(wait)
				c.mu.Unlock()
			}
			if attempt == retries {
				return decodeError(resp.StatusCode, data)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			c.noteInvalid(true)
			return decodeError(resp.StatusCode, data)
		case resp.StatusCode >= 300:
			return decodeError(resp.StatusCode, data)
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

// marshal is JSON with <, > and & as they are: a mention is <@id>, and what is sent reads as what is shown.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func decodeError(status int, data []byte) error {
	e := &Error{Status: status}
	_ = json.Unmarshal(data, e)
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// retryAfter is how long a 429 says to wait: the body's retry_after (seconds, with a fraction), else the header.
func retryAfter(h http.Header, data []byte) time.Duration {
	var body struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(data, &body) == nil && body.RetryAfter > 0 {
		return time.Duration(body.RetryAfter * float64(time.Second))
	}
	if s, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil {
		return time.Duration(s * float64(time.Second))
	}
	return time.Second
}

// wait holds a request back until the route's bucket and the global limit allow it.
func (c *REST) wait(ctx context.Context, key string) error {
	c.mu.Lock()
	cut := c.now().Add(-invalidWindow)
	for len(c.invalid) > 0 && c.invalid[0].Before(cut) {
		c.invalid = c.invalid[1:]
	}
	if len(c.invalid) >= c.invalidLimit {
		c.mu.Unlock()
		return ErrTooManyInvalid
	}
	b := c.buckets[key]
	if b == nil {
		b = &bucket{}
		c.buckets[key] = b
	}
	global := c.global
	c.mu.Unlock()
	if err := c.sleep(ctx, global.Sub(c.now())); err != nil {
		return err
	}
	if err := c.globalSlot(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.known && b.remaining <= 0 {
		if err := c.sleep(ctx, b.reset.Sub(c.now())); err != nil {
			return err
		}
		b.known = false // the next answer says where it stands
	}
	if b.known {
		b.remaining--
	}
	return nil
}

// globalSlot keeps to 50 requests a second.
func (c *REST) globalSlot(ctx context.Context) error {
	for {
		c.mu.Lock()
		now := c.now()
		if now.Sub(c.second) >= time.Second {
			c.second, c.count = now, 0
		}
		if c.count < globalPerSecond {
			c.count++
			c.mu.Unlock()
			return nil
		}
		next := c.second.Add(time.Second).Sub(now)
		c.mu.Unlock()
		if err := c.sleep(ctx, next); err != nil {
			return err
		}
	}
}

// learn keeps what an answer says of its limit. When Discord names the bucket the route shares, the limit moves there: other routes
// in the same bucket wait on it too.
func (c *REST) learn(method, path, key string, h http.Header) {
	remaining, err1 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	resetAfter, err2 := strconv.ParseFloat(h.Get("X-RateLimit-Reset-After"), 64)
	if err1 != nil || err2 != nil {
		return
	}
	c.mu.Lock()
	if hash := h.Get("X-RateLimit-Bucket"); hash != "" {
		_, template, majors := limitKeys(method, path)
		c.hashes[template] = hash
		key = hash + ":" + majors
	}
	b := c.buckets[key]
	if b == nil {
		b = &bucket{}
		c.buckets[key] = b
	}
	c.mu.Unlock()
	b.mu.Lock()
	b.remaining, b.reset, b.known = remaining, c.now().Add(time.Duration(resetAfter*float64(time.Second))), true
	b.mu.Unlock()
}

// retryable is a server error worth trying again: a gateway or an overloaded Discord. A message is posted again only when Discord
// cannot have taken it (502, 503): after a 500 or a 504 it may have, and a person would see it twice.
func retryable(method string, status int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	case http.StatusInternalServerError, http.StatusGatewayTimeout:
		return method != http.MethodPost
	}
	return false
}

// backoff is the wait before the next try of a request that failed on Discord's side.
func backoff(attempt int) time.Duration { return retryBase << attempt }

// retryBase is the first wait after a server error.
var retryBase = 500 * time.Millisecond

func (c *REST) noteInvalid(counts bool) {
	if !counts {
		return
	}
	c.mu.Lock()
	c.invalid = append(c.invalid, c.now())
	c.mu.Unlock()
}

// multipartBody is a message with files: the JSON in payload_json, the files in files[n].
type multipartBody struct {
	data        []byte
	contentType string
}

func multipartMessage(m MessageCreate) (*multipartBody, error) {
	for i, f := range m.Files {
		m.Attachments = append(m.Attachments, Attachment{ID: i, Filename: f.Name})
	}
	payload, err := marshal(m)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="payload_json"`}, "Content-Type": {"application/json"}})
	if err != nil {
		return nil, err
	}
	part.Write(payload)
	for i, f := range m.Files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[%d]"; filename=%q`, i, f.Name))
		h.Set("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, err
		}
		part.Write(f.Data)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return &multipartBody{data: buf.Bytes(), contentType: w.FormDataContentType()}, nil
}

// --- the endpoints the bridge uses -------------------------------------------------------------------------------------------------

// GatewayBot is where the gateway is, and how many sessions the bot may still start today.
type GatewayBot struct {
	URL               string `json:"url"`
	Shards            int    `json:"shards"`
	SessionStartLimit struct {
		Total          int `json:"total"`
		Remaining      int `json:"remaining"`
		ResetAfter     int `json:"reset_after"` // milliseconds
		MaxConcurrency int `json:"max_concurrency"`
	} `json:"session_start_limit"`
}

func (c *REST) GatewayBot(ctx context.Context) (GatewayBot, error) {
	var g GatewayBot
	return g, c.Do(ctx, http.MethodGet, "/gateway/bot", nil, &g)
}

// Application is the bot's application: its id is what commands are registered under.
type Application struct {
	ID   Snowflake `json:"id"`
	Name string    `json:"name"`
}

func (c *REST) Application(ctx context.Context) (Application, error) {
	var a Application
	return a, c.Do(ctx, http.MethodGet, "/applications/@me", nil, &a)
}

func (c *REST) CreateMessage(ctx context.Context, channel Snowflake, m MessageCreate) (Message, error) {
	var out Message
	var body any = m
	if len(m.Files) > 0 {
		mp, err := multipartMessage(m)
		if err != nil {
			return out, err
		}
		body = mp
	}
	return out, c.Do(ctx, http.MethodPost, "/channels/"+string(channel)+"/messages", body, &out)
}

// CreateDM opens the bot's DM channel with a user, or returns the one it has.
func (c *REST) CreateDM(ctx context.Context, user Snowflake) (Channel, error) {
	var out Channel
	return out, c.Do(ctx, http.MethodPost, "/users/@me/channels", map[string]any{"recipient_id": user}, &out)
}

func (c *REST) EditMessage(ctx context.Context, channel, message Snowflake, m MessageCreate) (Message, error) {
	var out Message
	return out, c.Do(ctx, http.MethodPatch, "/channels/"+string(channel)+"/messages/"+string(message), m, &out)
}

// StartThread opens a thread from a message.
func (c *REST) StartThread(ctx context.Context, channel, message Snowflake, name string) (Channel, error) {
	var out Channel
	return out, c.Do(ctx, http.MethodPost, "/channels/"+string(channel)+"/messages/"+string(message)+"/threads", map[string]any{"name": name}, &out)
}

func (c *REST) Channel(ctx context.Context, channel Snowflake) (Channel, error) {
	var out Channel
	return out, c.Do(ctx, http.MethodGet, "/channels/"+string(channel), nil, &out)
}

// CreateRole makes a role nobody holds, that people can mention: an agent's.
func (c *REST) CreateRole(ctx context.Context, guild Snowflake, name, reason string) (Role, error) {
	var out Role
	return out, c.do(ctx, http.MethodPost, "/guilds/"+string(guild)+"/roles", map[string]any{"name": name, "mentionable": true, "permissions": "0"}, &out, reason)
}

func (c *REST) DeleteRole(ctx context.Context, guild, role Snowflake, reason string) error {
	return c.do(ctx, http.MethodDelete, "/guilds/"+string(guild)+"/roles/"+string(role), nil, nil, reason)
}

// SetCommands replaces the application's global commands.
func (c *REST) SetCommands(ctx context.Context, app Snowflake, commands []Command) error {
	return c.Do(ctx, http.MethodPut, "/applications/"+string(app)+"/commands", commands, nil)
}

// SetGuildCommands replaces the application's commands in one guild: they apply at once, where global ones can take a while.
func (c *REST) SetGuildCommands(ctx context.Context, app, guild Snowflake, commands []Command) error {
	return c.Do(ctx, http.MethodPut, "/applications/"+string(app)+"/guilds/"+string(guild)+"/commands", commands, nil)
}

// Respond answers an interaction: within 3 seconds, or Discord tells the person it failed.
func (c *REST) Respond(ctx context.Context, i Interaction, r InteractionResponse) error {
	return c.Do(ctx, http.MethodPost, "/interactions/"+string(i.ID)+"/"+i.Token+"/callback", r, nil)
}

// EditOriginal replaces the answer to an interaction, such as a deferred one, for up to 15 minutes.
func (c *REST) EditOriginal(ctx context.Context, app Snowflake, token string, m MessageCreate) (Message, error) {
	var out Message
	return out, c.Do(ctx, http.MethodPatch, "/webhooks/"+string(app)+"/"+token+"/messages/@original", m, &out)
}

// FollowUp sends a message after an interaction was answered, for up to 15 minutes.
func (c *REST) FollowUp(ctx context.Context, app Snowflake, token string, m MessageCreate) (Message, error) {
	var out Message
	return out, c.Do(ctx, http.MethodPost, "/webhooks/"+string(app)+"/"+token, m, &out)
}
