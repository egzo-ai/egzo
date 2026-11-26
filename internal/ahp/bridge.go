// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package ahp is egzo's side of the Agent Host Protocol (https://github.com/microsoft/agent-host-protocol): the bridge `egzo agent ahp`
// runs inside an agent's container, between the harness's AHP server and stdin and stdout, and the filter the project's link puts on what
// passes, so that a hub reaches the chat of the agent's main session and nothing else.
//
// On stdin and stdout a message is one line of JSON: a JSON-RPC 2.0 message of AHP, or egzo's own notification of the main session
// (MainSessionMethod), which the bridge sends when it learns it and whenever it changes.
package ahp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// MainSessionMethod is egzo's notification of the agent's main session: an AHP extension (`x-` methods are the protocol's own place for
// them). Its params are MainSession.
const MainSessionMethod = "x-egzo/mainSession"

// MainSession names the main session of an agent, and its chat: the first session its harness created, the one egzo shows.
type MainSession struct {
	Session string `json:"session"`
	Chat    string `json:"chat"`
}

// MaxMessage is the longest message either way. A chat snapshot holds a session's turns, which can be long.
const MaxMessage = 16 << 20

// SessionURIs is how an AHP server of OpenCode names a session it did not create, and its default chat, from OpenCode's id of the session.
func SessionURIs(id string) MainSession {
	return MainSession{Session: "ahp-session:/" + id, Chat: "ahp-chat:/" + id}
}

// Bridge connects to the AHP server at url with token and copies messages between it and in and out until either ends. mainFile is
// where the harness writes its main session's id; the bridge reads it now and then, and says what it finds.
//
// The client's connection carries the client's messages and the server's, nothing else: the bridge learns whether the server knows the
// main session on a connection of its own. The server answers a connection only once it initialized, and the client must hear of the main
// session before it can reconnect (the link lets a reconnect name the main session's channels only once it knows it): asking on the
// client's connection would wait for the client, which waits for the bridge.
func Bridge(ctx context.Context, url, token, mainFile string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, _, err := dial(ctx, url, token)
	if err != nil {
		return fmt.Errorf("the agent's AHP server does not answer (%s): %w", url, err)
	}
	conn.SetReadLimit(MaxMessage)
	defer conn.CloseNow()

	var mu sync.Mutex
	emit := func(line []byte) error {
		mu.Lock()
		defer mu.Unlock()
		_, err := out.Write(append(line, '\n'))
		return err
	}
	errs := make(chan error, 3)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				errs <- nil
				return
			}
			if err := emit(compact(data)); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64<<10), MaxMessage)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, line); err != nil {
				errs <- nil
				return
			}
		}
		errs <- scanner.Err()
	}()
	go func() {
		if err := watchMain(ctx, url, token, mainFile, emit); err != nil {
			errs <- err
		}
	}()
	err = <-errs
	conn.Close(websocket.StatusNormalClosure, "")
	return err
}

// bridgeClientID is the client the bridge is to the server on its own connection: one name for every bridge of the container, so the
// server's record of its clients does not grow with each connection a hub makes.
const bridgeClientID = "egzo-bridge"

// watchMain says the main session once the server lists it, and again whenever it changes: egzo names it before the harness makes it,
// and the server learns of a session it did not make late, or when it is listed. Then a client can subscribe to it as soon as it hears.
// It asks on a connection of its own, made again if it drops. An error is what emit returned.
func watchMain(ctx context.Context, url, token, mainFile string, emit func([]byte) error) error {
	said := ""
	for ctx.Err() == nil {
		var err error
		said, err = watchOn(ctx, url, token, mainFile, said, emit)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

// watchOn is watchMain on one connection, until it drops: it answers what was said last.
func watchOn(ctx context.Context, url, token, mainFile, said string, emit func([]byte) error) (string, error) {
	conn, _, err := dial(ctx, url, token)
	if err != nil {
		return said, nil
	}
	conn.SetReadLimit(MaxMessage)
	defer conn.CloseNow()
	next := 0
	// ask sends a request and waits for its answer, skipping what else the server says
	ask := func(method string, params any) (json.RawMessage, bool) {
		next++
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": next, "method": method, "params": params})
		askCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if conn.Write(askCtx, websocket.MessageText, request) != nil {
			return nil, false
		}
		for {
			_, data, err := conn.Read(askCtx)
			if err != nil {
				return nil, false
			}
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(data, &m) == nil && m.ID != nil && *m.ID == next {
				return m.Result, true
			}
		}
	}
	if _, ok := ask("initialize", map[string]any{"channel": RootChannel, "clientId": bridgeClientID, "protocolVersions": []string{"1.0.0"},
		"initialSubscriptions": []string{}}); !ok {
		return said, nil
	}
	for {
		if id := readMain(mainFile); id != "" && id != said {
			result, ok := ask("listSessions", map[string]any{})
			if !ok {
				return said, nil
			}
			if listed(result, SessionURIs(id).Session) {
				said = id
				note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": MainSessionMethod, "params": SessionURIs(id)})
				if err := emit(note); err != nil {
					return said, err
				}
			}
		}
		// until it is said, ask again soon
		wait := time.Second
		if said == "" {
			wait = 250 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return said, nil
		case <-time.After(wait):
		}
	}
}

// listed says whether a listSessions result lists session.
func listed(result json.RawMessage, session string) bool {
	var r struct {
		Items []struct {
			Resource string `json:"resource"`
		} `json:"items"`
	}
	if json.Unmarshal(result, &r) != nil {
		return false
	}
	for _, item := range r.Items {
		if item.Resource == session {
			return true
		}
	}
	return false
}

func dial(ctx context.Context, url, token string) (*websocket.Conn, *http.Response, error) {
	header := http.Header{"Authorization": {"Bearer " + token}}
	var lastErr error
	// The server starts with the harness, a little after the container: wait for it a while.
	deadline := time.Now().Add(30 * time.Second)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, resp, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPHeader: header})
		cancel()
		if err == nil {
			return conn, resp, nil
		}
		lastErr = err
		if resp != nil && resp.StatusCode != 0 {
			return nil, resp, err // the server is there and said no: waiting changes nothing
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, nil, lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func readMain(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if !validSessionID(id) {
		return ""
	}
	return id
}

// validSessionID takes OpenCode's ids of sessions: letters, digits and underscores.
func validSessionID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// compact makes a message one line: JSON from the server may span lines.
func compact(data []byte) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, data); err != nil {
		return []byte(strings.NewReplacer("\n", " ", "\r", " ").Replace(string(data)))
	}
	return b.Bytes()
}
