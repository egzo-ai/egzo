// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egzo-ai/egzo/internal/names"
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

	// Who, once the proxy credentials (or, for ssh, the key's signature) are checked: the agent's name, its instance's id and the
	// profile that decided. Never set otherwise.
	Agent    string `json:"agent,omitempty"`
	Instance string `json:"instance,omitempty"`
	Profile  string `json:"profile,omitempty"`
	// ClaimedAgent is the name the client sent in its proxy credentials, right or wrong (https only: an ssh client sends no name).
	ClaimedAgent string `json:"claimed_agent,omitempty"`
	// Client is the address the connection came from, and UserAgent what the client says it is: the User-Agent of a CONNECT, an
	// ssh client's identification string. What the proxy saw, never proof of who.
	Client    string `json:"client,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`

	Host string `json:"host,omitempty"`
	// Service is the service of the profile that covers the host, on an allowed https connect.
	Service string `json:"service,omitempty"`
	// Server is an ssh server's name in the profile.
	Server string `json:"server,omitempty"`

	// On a request: never the query string. Path is decoded, as the agent wrote it (before a placeholder is swapped);
	// RawPath is the same path as it went on the wire, when its escapes make it differ (%2F is a '/' in Path).
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	RawPath string `json:"raw_path,omitempty"`
	Status  int    `json:"status,omitempty"`
	// Injected is the service whose credential the proxy set on the request; none when it set none. Substituted are the
	// services whose placeholder it swapped for their secret.
	Injected    string   `json:"injected,omitempty"`
	Substituted []string `json:"substituted,omitempty"`
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
	CodePath           Code = "path"            // a path under a service's paths that could mean another one
	CodeOutsidePaths   Code = "outside-paths"   // why an allowed request went without the credential of its host's service
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

// MaxField bounds every string an agent can influence: it is logged for each denied attempt, and an
// unbounded value would let one agent fill the log. A clipped field ends in an ellipsis.
const MaxField = 256

const ellipsis = "…"

func clip(text string) string {
	if len(text) <= MaxField {
		return text
	}
	cut := MaxField
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + ellipsis
}

// clipped reports whether text is what clip leaves: text of at most MaxField bytes and an ellipsis.
func clipped(text string) bool { return len(text) <= MaxField+len(ellipsis) && utf8.ValidString(text) }

// maxSubstituted bounds the services a reader takes in one request's substituted: one per service of a profile.
const maxSubstituted = 64

func allClipped(texts []string) bool {
	for _, text := range texts {
		if !clipped(text) {
			return false
		}
	}
	return true
}

// connID is the form of a connection's id (newConnID).
var connID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ErrEvent is an event the proxy never writes: a field it never fills that way.
type ErrEvent struct{ Field, Value string }

func (e ErrEvent) Error() string {
	return fmt.Sprintf("audit event field %s is %.60q, which the proxy never writes", e.Field, e.Value)
}

// Check reports whether e is an event as the proxy writes it (Log): every name by the rules of internal/names, every text clipped,
// no count below zero. A reader of a log it does not control (the hub) keeps only what passes.
func (e Event) Check() error {
	optional := func(value string, valid func(string) bool) bool { return value == "" || valid(value) }
	code := ""
	reason := true
	if e.Reason != nil {
		code = string(e.Reason.Code)
		reason = names.Identifier(code) && clipped(e.Reason.Text)
	}
	for _, check := range []struct {
		field, value string
		ok           bool
	}{
		{"conn", e.Conn, connID.MatchString(e.Conn)},
		{"time", e.Time.String(), !e.Time.IsZero()},
		{"protocol", string(e.Protocol), names.Identifier(string(e.Protocol))},
		{"event", string(e.Kind), names.Identifier(string(e.Kind))},
		{"decision", string(e.Decision), optional(string(e.Decision), names.Identifier)},
		{"mode", string(e.Mode), optional(string(e.Mode), names.Identifier)},
		{"agent", e.Agent, optional(e.Agent, names.Instance)},
		{"instance", e.Instance, optional(e.Instance, names.InstanceID)},
		{"profile", e.Profile, clipped(e.Profile)},
		{"claimed_agent", e.ClaimedAgent, optional(e.ClaimedAgent, names.Instance)},
		{"client", e.Client, optional(e.Client, func(a string) bool { return net.ParseIP(a) != nil })},
		{"user_agent", e.UserAgent, clipped(e.UserAgent)},
		{"host", e.Host, optional(e.Host, names.Host)},
		{"server", e.Server, optional(e.Server, names.SSHServer)},
		{"method", e.Method, clipped(e.Method)},
		{"path", e.Path, clipped(e.Path)},
		{"raw_path", e.RawPath, clipped(e.RawPath)},
		{"service", e.Service, clipped(e.Service)},
		{"injected", e.Injected, clipped(e.Injected)},
		{"substituted", strings.Join(e.Substituted, ","), len(e.Substituted) <= maxSubstituted && allClipped(e.Substituted)},
		{"command", e.Command, clipped(e.Command)},
		{"reason", code, reason},
		{"counts", fmt.Sprint(e.Status, e.BytesUp, e.BytesDown, e.DurationMs), e.Status >= 0 && e.BytesUp >= 0 && e.BytesDown >= 0 && e.DurationMs >= 0},
	} {
		if !check.ok {
			return ErrEvent{Field: check.field, Value: check.value}
		}
	}
	return nil
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
	event.Agent, event.ClaimedAgent, event.Host, event.Path = clip(event.Agent), clip(event.ClaimedAgent), clip(event.Host), clip(event.Path)
	event.RawPath = clip(event.RawPath)
	event.UserAgent = clip(event.UserAgent)
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

// conn is one connection as the audit sees it: what every event of it carries. The fields after client are filled in as
// they become known (who it is from once the credentials are checked, the server once an ssh user is signed in).
type conn struct {
	audit     *Audit
	id        string
	protocol  Protocol
	client    string
	userAgent string
	claimed   string
	agent     string
	instance  string
	profile   string
	host      string
	service   string // the service of the profile that covers host, once decided
	server    string
}

// newConn starts the audit of a connection from remote, the client's address and port.
func newConn(audit *Audit, protocol Protocol, remote string) *conn {
	return &conn{audit: audit, id: newConnID(), protocol: protocol, client: clientAddress(remote)}
}

// clientAddress is the address of remote without its port, or empty when it has none.
func clientAddress(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// claims records the name the client sent in its credentials, when it is a name an instance can have: anything else may be a
// token typed in the wrong place, and is not logged.
func (c *conn) claims(agent string) {
	if names.Instance(agent) {
		c.claimed = agent
	}
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
	e.Client, e.UserAgent, e.ClaimedAgent = c.client, c.userAgent, c.claimed
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
