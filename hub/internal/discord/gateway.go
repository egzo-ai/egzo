// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Gateway opcodes.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// closeResume is the code the client closes with when it means to resume: 1000 and 1001 end the session.
const closeResume websocket.StatusCode = 4900

// maxPayload is the largest event read: a big guild's GUILD_CREATE, uncompressed.
const maxPayload = 32 << 20

// FatalError is a close that trying again cannot fix: a wrong token, intents the bot may not have.
type FatalError struct {
	Code   int
	Reason string
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("discord gateway closed with %d: %s", e.Code, e.Reason)
}

// fatalToken is the close code for a wrong token.
const fatalToken = 4004

// fatal are the close codes after which the client stops.
var fatal = map[int]string{
	fatalToken: "the token is wrong",
	4010:       "an invalid shard",
	4011:       "sharding is required",
	4012:       "an API version Discord does not speak",
	4013:       "intents that do not exist",
	4014:       "intents the bot may not have: enable Message Content in the Developer Portal",
}

// sessionStartMargin is how many session starts the client leaves unspent: Discord resets the bot's token when the daily limit is
// hit, and its documentation does not say whether spending the last one counts.
const sessionStartMargin = 5

// fresh are the close codes after which the session cannot be resumed, and the client identifies again.
var fresh = map[int]bool{4007: true, 4009: true}

// Gateway is one connection to Discord's gateway, kept up: it resumes after a drop, so no event is lost, and identifies again when the
// session cannot be resumed.
type Gateway struct {
	Token   string
	Intents int
	// URL is where the gateway is, from GatewayBot; the session's resume URL replaces it while there is a session.
	URL     string
	Handler func(Event) // called for every event, in order, from one goroutine
	Log     *slog.Logger
	// Sessions says how many sessions the bot may still start, and when that renews: REST.GatewayBot. Before identifying (never before a
	// resume) the client asks, and waits for the renewal when only a few are left; a token it refuses stops the client. nil asks nothing.
	Sessions func(context.Context) (GatewayBot, error)

	mu        sync.Mutex
	sessionID string
	resumeURL string
	seq       int64
	stats     Stats
	conn      *websocket.Conn // the connection now, for drop
}

// Stats counts what the connection went through.
type Stats struct {
	Connects, Identifies, Resumes, Zombies int
}

// Stats says what the connection went through so far.
func (g *Gateway) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Run keeps the connection until ctx ends, or Discord closes it in a way that trying again cannot fix.
func (g *Gateway) Run(ctx context.Context) error {
	if g.Log == nil {
		g.Log = slog.New(slog.DiscardHandler)
	}
	delay := time.Second
	for {
		began := time.Now()
		err := g.connect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var f *FatalError
		if errors.As(err, &f) {
			return err
		}
		g.Log.Info("discord gateway: connecting again", "err", err)
		if time.Since(began) > time.Minute {
			delay = time.Second
		}
		if err := sleep(ctx, delay+rand.N(delay/2+1)); err != nil {
			return nil
		}
		delay = min(delay*2, time.Minute)
	}
}

type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// connect is one connection, from the dial to its end.
func (g *Gateway) connect(ctx context.Context) error {
	g.mu.Lock()
	address, resuming := g.URL, g.sessionID != ""
	if resuming && g.resumeURL != "" {
		address = g.resumeURL
	}
	g.stats.Connects++
	g.mu.Unlock()
	conn, _, err := websocket.Dial(ctx, strings.TrimSuffix(address, "/")+fmt.Sprintf("/?v=%d&encoding=json", APIVersion), nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(maxPayload)
	defer conn.CloseNow()
	g.mu.Lock()
	g.conn = conn
	g.mu.Unlock()

	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	first, err := read(ctx, conn)
	if err != nil {
		return err
	}
	if first.Op != opHello || json.Unmarshal(first.D, &hello) != nil || hello.HeartbeatInterval <= 0 {
		return errors.New("discord gateway: no hello")
	}
	if resuming {
		g.mu.Lock()
		resume := map[string]any{"token": g.Token, "session_id": g.sessionID, "seq": g.seq}
		g.stats.Resumes++
		g.mu.Unlock()
		err = send(ctx, conn, opResume, resume)
	} else {
		if err := g.budget(ctx); err != nil {
			return err
		}
		g.mu.Lock()
		g.stats.Identifies++
		g.mu.Unlock()
		err = send(ctx, conn, opIdentify, map[string]any{
			"token": g.Token, "intents": g.Intents,
			"properties": map[string]string{"os": "linux", "browser": "egzo", "device": "egzo"},
		})
	}
	if err != nil {
		return err
	}

	acked := make(chan struct{}, 1)
	beatNow := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	go g.heartbeat(ctx, conn, time.Duration(hello.HeartbeatInterval)*time.Millisecond, acked, beatNow, stop)

	for {
		p, err := read(ctx, conn)
		if err != nil {
			return g.closed(err)
		}
		switch p.Op {
		case opDispatch:
			g.dispatch(p)
		case opHeartbeat:
			select {
			case beatNow <- struct{}{}:
			default:
			}
		case opHeartbeatACK:
			select {
			case acked <- struct{}{}:
			default:
			}
		case opReconnect:
			conn.Close(closeResume, "reconnect")
			return errors.New("discord asked to reconnect")
		case opInvalidSession:
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			if !resumable {
				g.forget()
			}
			conn.Close(closeResume, "invalid session")
			// Discord asks for a wait of 1 to 5 seconds before identifying again.
			_ = sleep(ctx, time.Second+rand.N(4*time.Second))
			return errors.New("discord said the session is invalid")
		}
	}
}

// budget waits until the bot may start a session: Discord allows so many a day (1000 for most bots), and hitting the limit ends every
// session and resets the bot's token. A wrong token stops here, before a session is spent.
func (g *Gateway) budget(ctx context.Context) error {
	if g.Sessions == nil {
		return nil
	}
	limits, err := g.Sessions(ctx)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusUnauthorized { // the same wrong token identify would send: 4004 before the dial
		return &FatalError{Code: fatalToken, Reason: fatal[fatalToken]}
	}
	if err != nil {
		return err
	}
	if limits.SessionStartLimit.Remaining > sessionStartMargin {
		return nil
	}
	wait := time.Duration(limits.SessionStartLimit.ResetAfter) * time.Millisecond
	g.Log.Warn("discord gateway: almost no session starts left today, waiting for the reset", "left", limits.SessionStartLimit.Remaining, "wait", wait)
	return sleep(ctx, wait)
}

// dispatch keeps the sequence number and what READY says of the session, and hands the event on.
func (g *Gateway) dispatch(p payload) {
	g.mu.Lock()
	if p.S != nil {
		g.seq = *p.S
	}
	if p.T == "READY" {
		var ready struct {
			SessionID        string `json:"session_id"`
			ResumeGatewayURL string `json:"resume_gateway_url"`
		}
		_ = json.Unmarshal(p.D, &ready)
		g.sessionID, g.resumeURL = ready.SessionID, ready.ResumeGatewayURL
	}
	seq := g.seq
	g.mu.Unlock()
	if g.Handler != nil {
		g.Handler(Event{Type: p.T, Seq: seq, Data: p.D})
	}
}

// heartbeat beats at the interval, the first after a random part of it, and closes a connection whose last beat was not acknowledged: a
// zombie, which hears nothing and would never say so.
func (g *Gateway) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration, acked, beatNow <-chan struct{}, stop <-chan struct{}) {
	timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
	defer timer.Stop()
	waiting := false
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-acked:
			waiting = false
			continue
		case <-beatNow:
		case <-timer.C:
			if waiting {
				g.mu.Lock()
				g.stats.Zombies++
				g.mu.Unlock()
				conn.CloseNow() // at once: a zombie would never answer a close, and no 1000 keeps the session
				return
			}
			timer.Reset(interval)
		}
		g.mu.Lock()
		seq := g.seq
		g.mu.Unlock()
		var d any
		if seq > 0 {
			d = seq
		}
		if send(ctx, conn, opHeartbeat, d) != nil {
			return
		}
		waiting = true
	}
}

// closed says what the end of a connection means: fatal, a session to forget, or one to resume.
func (g *Gateway) closed(err error) error {
	code := int(websocket.CloseStatus(err))
	if reason, ok := fatal[code]; ok {
		return &FatalError{Code: code, Reason: reason}
	}
	if fresh[code] {
		g.forget()
	}
	return err
}

// drop cuts the connection now, as a network would: the client resumes. For tests against the real Discord.
func (g *Gateway) drop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil {
		g.conn.CloseNow()
	}
}

func (g *Gateway) forget() {
	g.mu.Lock()
	g.sessionID, g.resumeURL, g.seq = "", "", 0
	g.mu.Unlock()
}

func read(ctx context.Context, conn *websocket.Conn) (payload, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return payload{}, err
	}
	var p payload
	return p, json.Unmarshal(data, &p)
}

func send(ctx context.Context, conn *websocket.Conn, op int, d any) error {
	data, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}
