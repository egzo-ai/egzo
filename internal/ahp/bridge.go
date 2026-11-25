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
	var writing sync.Mutex // the client's lines and the bridge's own requests share the connection
	send := func(line []byte) error {
		writing.Lock()
		defer writing.Unlock()
		return conn.Write(ctx, websocket.MessageText, line)
	}
	lists := make(chan []string, 1) // the sessions the server listed, for the bridge's own listSessions
	errs := make(chan error, 3)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				errs <- nil
				return
			}
			if listed, ours := ownList(data); ours {
				select {
				case lists <- listed:
				default:
				}
				continue
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
			if err := send(line); err != nil {
				errs <- nil
				return
			}
		}
		errs <- scanner.Err()
	}()
	go func() {
		// The main session is said once the server lists it: egzo names it before the harness makes it, and the server learns of a
		// session it did not make late, or when it is listed. Then a client can subscribe to it as soon as it hears.
		said, asked := "", 0
		for {
			if id := readMain(mainFile); id != "" && id != said {
				asked++
				request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": fmt.Sprintf("%s%d", ownListID, asked), "method": "listSessions", "params": map[string]any{}})
				if send(request) == nil && listedIn(ctx, lists, SessionURIs(id).Session) {
					said = id
					note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": MainSessionMethod, "params": SessionURIs(id)})
					if err := emit(note); err != nil {
						errs <- err
						return
					}
				}
			}
			// until it is said, ask again soon: the server answers a list only once the client initialized, a moment after the bridge
			// connected
			wait := time.Second
			if said == "" {
				wait = 250 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
	err = <-errs
	conn.Close(websocket.StatusNormalClosure, "")
	return err
}

// ownListID starts the id of the listSessions the bridge asks for its own sake: their answers stay in the bridge.
const ownListID = "x-egzo/list-"

// ownList says whether a message from the server answers one of the bridge's own listSessions, and which sessions it listed.
func ownList(data []byte) (sessions []string, ours bool) {
	var m struct {
		ID     string `json:"id"`
		Result struct {
			Items []struct {
				Resource string `json:"resource"`
			} `json:"items"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &m) != nil || !strings.HasPrefix(m.ID, ownListID) {
		return nil, false
	}
	for _, item := range m.Result.Items {
		sessions = append(sessions, item.Resource)
	}
	return sessions, true
}

// listedIn waits a while for the answer to the bridge's listSessions, and says whether it lists session.
func listedIn(ctx context.Context, lists <-chan []string, session string) bool {
	select {
	case listed := <-lists:
		for _, s := range listed {
			if s == session {
				return true
			}
		}
		return false
	case <-time.After(5 * time.Second):
		return false
	case <-ctx.Done():
		return false
	}
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
