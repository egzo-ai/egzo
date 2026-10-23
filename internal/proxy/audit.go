// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

// Event is one line of the audit log: a step of one connection, and what the proxy decided about it. Bodies and query
// strings are never recorded. See roadmap/proxy-audit.md for the schema.
type Event struct {
	Time time.Time `json:"time"`
	// Conn is the connection: every event of one connection has the same.
	Conn     string   `json:"conn"`
	Protocol Protocol `json:"protocol"`
	Kind     Kind     `json:"event"`
	Decision Outcome  `json:"decision,omitempty"` // none on a close
	Mode     Mode     `json:"mode,omitempty"`     // on an allowed https connect

	// Who, once the proxy credentials are checked: the agent's name, its instance's id and the profile that decided.
	Agent    string `json:"agent,omitempty"`
	Instance string `json:"instance,omitempty"`
	Profile  string `json:"profile,omitempty"`

	Host string `json:"host,omitempty"`
	// Server is an ssh server's name in the profile.
	Server string `json:"server,omitempty"`

	// On a request: never the query string.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	// Command is what an agent asked an ssh server to run.
	Command string `json:"command,omitempty"`

	Reason *Reason `json:"reason,omitempty"`

	// On a close: what went from the agent to the host and back, and for how long.
	BytesUp    int64 `json:"bytes_up,omitempty"`
	BytesDown  int64 `json:"bytes_down,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// Protocol is what a connection speaks.
type Protocol string

const (
	HTTPS Protocol = "https"
	SSH   Protocol = "ssh"
)

// Kind is the step of a connection an event is about.
type Kind string

const (
	KindConnect Kind = "connect" // the connection was opened: the decision about it
	KindRequest Kind = "request" // a request inside a terminated https connection
	KindClose   Kind = "close"   // the end of a connection that was allowed, with the bytes and the duration
	// KindHandshake is an https connection to a terminated host whose TLS handshake with the proxy failed.
	KindHandshake Kind = "handshake"
	// KindUpstream is an ssh connection whose server could not be reached or would not open a session.
	KindUpstream Kind = "upstream"
	KindExec     Kind = "exec"    // an ssh command
	KindShell    Kind = "shell"   // an ssh shell
	KindSFTP     Kind = "sftp"    // an sftp session (scp too)
	KindChannel  Kind = "channel" // an ssh channel that is not a session
)

// Outcome is what the proxy decided.
type Outcome string

const (
	OutcomeAllow Outcome = "allow"
	OutcomeDeny  Outcome = "deny"
	OutcomeError Outcome = "error"
	// OutcomeChallenge is a connection without proxy credentials, answered 407: browsers start that way. Not a denial.
	OutcomeChallenge Outcome = "challenge"
)

// Mode is how an allowed https connection is handled.
type Mode string

const (
	ModeTunnel    Mode = "tunnel"    // the proxy sees the host name, never the traffic
	ModeInject    Mode = "inject"    // TLS is terminated to add a credential or swap a placeholder
	ModeInspect   Mode = "inspect"   // TLS is terminated to log the requests
	ModeIntercept Mode = "intercept" // TLS is terminated because the client sent Encrypted Client Hello
)

// Reason says why: a code from a closed list, for tools, and a text for people, which may change.
type Reason struct {
	Code Code   `json:"code"`
	Text string `json:"text"`
}

// Code is why the proxy decided what it did. The list is closed: a tool explains or fixes a denial by its code.
type Code string

const (
	CodeNoPolicy       Code = "no-policy"       // the proxy was restarted and has no policy
	CodeNoCredentials  Code = "no-credentials"  // no proxy credentials yet (a challenge)
	CodeCredentials    Code = "credentials"     // wrong proxy credentials
	CodePort           Code = "port"            // only port 443 is allowed
	CodeNotInProfile   Code = "not-in-profile"  // the host is not in the agent's profile
	CodePrivateAddress Code = "private-address" // the host is a private address
	CodeLimit          Code = "limit"           // too many connections
	CodeClientHello    Code = "client-hello"    // the client's TLS hello could not be read
	CodeSNIMismatch    Code = "sni-mismatch"    // the TLS server name differs from the CONNECT host
	CodeHost           Code = "host"            // the CONNECT target is not a host name or address
	CodeHostHeader     Code = "host-header"     // a request's Host header is malformed
	CodeHostMismatch   Code = "host-mismatch"   // a request names another host than its connection
	CodePlaceholder    Code = "placeholder"     // a placeholder could not be swapped
	CodeAborted        Code = "aborted"         // the client left before the handshake
	CodeDial           Code = "dial"            // the upstream did not answer
	CodeTLSHandshake   Code = "tls-handshake"   // the client's TLS handshake with the proxy failed
	CodeUpstream       Code = "upstream"        // a request to the upstream failed
	CodeECH            Code = "ech"             // why a connection is intercepted
	CodeIdentity       Code = "identity"        // ssh: no valid identity for a server of a profile
	CodeChannel        Code = "channel"         // ssh: a channel that is not a session
	CodeForwarding     Code = "forwarding"      // ssh: a global request
	CodeRequest        Code = "request"         // ssh: a session request that is not relayed
	CodeHostKey        Code = "host-key"        // ssh: the server's host key is not the pinned one, or the pin is not usable
	CodeKey            Code = "key"             // ssh: the profile's key is not usable
	CodeAuth           Code = "auth"            // ssh: the server refused the key
	CodeSession        Code = "session"         // ssh: the server did not open a session
	CodeConfig         Code = "config"          // ssh: the listener has no host key
	CodeIdle           Code = "idle"            // ssh: closed after being idle
)

func because(code Code, text string) *Reason { return &Reason{Code: code, Text: text} }

// maxField bounds every string an agent can influence: it is logged for each denied attempt, and an
// unbounded value would let one agent fill the log.
const maxField = 256

func clip(text string) string {
	if len(text) <= maxField {
		return text
	}
	cut := maxField
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// Audit writes events as JSON lines.
type Audit struct {
	mu  sync.Mutex
	out io.Writer
}

func NewAudit(out io.Writer) *Audit { return &Audit{out: out} }

func (a *Audit) Log(event Event) {
	if a == nil || a.out == nil {
		return
	}
	event.Time = time.Now().UTC()
	event.Agent, event.Host, event.Path = clip(event.Agent), clip(event.Host), clip(event.Path)
	event.Server, event.Command = clip(event.Server), clip(event.Command)
	if event.Reason != nil {
		event.Reason = &Reason{Code: event.Reason.Code, Text: clip(event.Reason.Text)}
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.out.Write(append(line, '\n'))
}

// conn is one connection as the audit sees it: what every event of it carries. The fields after protocol are filled in as
// they become known (who it is from once the credentials are checked, the server once an ssh user is signed in).
type conn struct {
	audit    *Audit
	id       string
	protocol Protocol
	agent    string
	instance string
	profile  string
	host     string
	server   string
}

func newConn(audit *Audit, protocol Protocol) *conn {
	return &conn{audit: audit, id: newConnID(), protocol: protocol}
}

// newConnID is 64 random bits: no two connections share one, across restarts too.
func newConnID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// from says who the connection is from.
func (c *conn) from(agent, instance, profile string) {
	c.agent, c.instance, c.profile = agent, instance, profile
}

// log writes e as an event of this connection.
func (c *conn) log(e Event) {
	e.Conn, e.Protocol = c.id, c.protocol
	e.Agent, e.Instance, e.Profile = c.agent, c.instance, c.profile
	if e.Host == "" {
		e.Host = c.host
	}
	if e.Server == "" {
		e.Server = c.server
	}
	c.audit.Log(e)
}

// decide logs a decision about step k.
func (c *conn) decide(k Kind, decision Outcome, reason *Reason) {
	c.log(Event{Kind: k, Decision: decision, Reason: reason})
}

// closed logs the end of an allowed connection.
func (c *conn) closed(up, down int64, started time.Time, reason *Reason) {
	c.log(Event{Kind: KindClose, BytesUp: up, BytesDown: down, DurationMs: time.Since(started).Milliseconds(), Reason: reason})
}
