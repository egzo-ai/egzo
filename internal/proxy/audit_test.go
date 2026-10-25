// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What a hostile client sends is written as Check says the proxy writes: a reader of the trail can refuse anything else.
func TestEveryEventTheProxyWritesPassesCheck(t *testing.T) {
	r := newRig(t, nil)
	r.setPolicy("coder", "tok", []string{"example.com"})
	send := func(target, login string) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(r.front.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
		if login != "" {
			request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(login)) + "\r\n"
		}
		io.WriteString(conn, request+"\r\n")
		if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
			t.Fatal(err)
		}
	}
	junk := strings.Repeat("x", 64) + ".example.com"
	send("example.org:443", "coder:tok")
	send(junk+":443", "coder:tok")
	send(junk+":443", "")
	send("example.com:22", "coder:tok")
	send("example.com:443", "Not An Agent "+strings.Repeat("é", 300)+":tok")
	send("example.com:443", "coder:nope")

	lines := strings.Split(strings.TrimSpace(r.audit.String()), "\n")
	if len(lines) < 6 {
		t.Fatalf("%d events:\n%s", len(lines), r.audit.String())
	}
	codes := map[Code]bool{}
	for _, line := range lines {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if err := e.Check(); err != nil {
			t.Errorf("%v\n%s", err, line)
		}
		if e.Reason != nil {
			codes[e.Reason.Code] = true
		}
	}
	if !codes[CodeHost] || !codes[CodeCredentials] || !codes[CodeNotInProfile] || !codes[CodePort] {
		t.Errorf("codes = %v", codes)
	}
}

func TestCheckRefusesWhatTheProxyNeverWrites(t *testing.T) {
	good := Event{Time: time.Now(), Conn: "0123456789abcdef", Protocol: HTTPS, Kind: KindConnect, Decision: OutcomeDeny, Agent: "coder-1",
		Instance: "01JAB3C4D5E6F7G8H9JKMNPQRS", Profile: "default", ClaimedAgent: "coder-1", Client: "10.89.3.2", UserAgent: "curl/8.11.1",
		Host: "example.com", Reason: because(CodeNotInProfile, "not in the profile")}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for field, bad := range map[string]func(*Event){
		"conn":          func(e *Event) { e.Conn = "x" },
		"time":          func(e *Event) { e.Time = time.Time{} },
		"agent":         func(e *Event) { e.Agent = "<img src=x>" },
		"instance":      func(e *Event) { e.Instance = "nope" },
		"claimed_agent": func(e *Event) { e.ClaimedAgent = "a token:typed here" },
		"client":        func(e *Event) { e.Client = "proxy:3128" },
		"user_agent":    func(e *Event) { e.UserAgent = strings.Repeat("u", MaxField+4) },
		"host":          func(e *Event) { e.Host = strings.Repeat("x", 300) },
		"server":        func(e *Event) { e.Server = "root" },
		"path":          func(e *Event) { e.Path = strings.Repeat("/", MaxField+4) },
		"reason":        func(e *Event) { e.Reason = because("Not A Code", "") },
		"counts":        func(e *Event) { e.BytesUp = -1 },
	} {
		e := good
		bad(&e)
		var refused ErrEvent
		if err := e.Check(); !errors.As(err, &refused) || refused.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
}
