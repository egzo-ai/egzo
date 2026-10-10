// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 10 * time.Second
	allowedPort      = "443"
)

// Server is the CONNECT proxy agents use for all egress.
type Server struct {
	minter *Minter
	audit  *Audit
	policy atomic.Pointer[Policy]
	// agents maps each bound agent to its credentials and profile. It is replaced as a whole on every
	// change (agentsMu orders the changes), so a connection reads it without a lock.
	agents   atomic.Pointer[map[string]Binding]
	agentsMu sync.Mutex

	// Transport reaches the real upstream for intercepted hosts. Tests replace it.
	Transport http.RoundTripper
	// Dial reaches the real upstream for tunnelled hosts. Tests replace it.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)

	// MaxPerAgent and MaxTotal bound the connections one agent, and all of them, may hold open;
	// IdleTimeout closes a connection that has moved no data for that long.
	MaxPerAgent int
	MaxTotal    int
	IdleTimeout time.Duration
	limits      limiter
}

func NewServer(ca *CA, audit *Audit) *Server {
	dialer := &net.Dialer{Timeout: dialTimeout, Control: publicOnly}
	return &Server{
		MaxPerAgent: defaultMaxPerAgent,
		MaxTotal:    defaultMaxTotal,
		IdleTimeout: defaultIdleTimeout,
		minter:      NewMinter(ca),
		audit:       audit,
		Transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   handshakeTimeout,
			ResponseHeaderTimeout: 5 * time.Minute,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		},
		Dial: dialer.DialContext,
	}
}

// SetCA switches the CA that signs the certificates of intercepted hosts.
func (s *Server) SetCA(ca *CA) { s.minter.SetCA(ca) }

// SetPolicy atomically replaces the profiles. Until one is set, everything is denied. The agents bound
// to profiles stay bound: a changed profile takes effect for them at once.
func (s *Server) SetPolicy(policy *Policy) { s.policy.Store(policy) }

// BindAgent lets an agent use a profile of the loaded policy, under a token. The policy comes first:
// a binding to a profile nobody loaded would silently deny everything.
func (s *Server) BindAgent(name string, binding Binding) error {
	if binding.Token == "" {
		return errors.New("a binding needs a token")
	}
	policy := s.policy.Load()
	if policy == nil {
		return errors.New("no egress policy is loaded: run `egzo up` first")
	}
	if _, ok := policy.Profiles[binding.Profile]; !ok {
		return fmt.Errorf("the loaded policy has no egress profile %q", binding.Profile)
	}
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()
	next := map[string]Binding{name: binding}
	if current := s.agents.Load(); current != nil {
		for key, value := range *current {
			if key != name {
				next[key] = value
			}
		}
	}
	s.agents.Store(&next)
	return nil
}

// UnbindAgent removes an agent's credentials: it cannot reach anything through the proxy again. Unknown
// names are fine.
func (s *Server) UnbindAgent(name string) {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()
	current := s.agents.Load()
	if current == nil {
		return
	}
	next := make(map[string]Binding, len(*current))
	for key, value := range *current {
		if key != name {
			next[key] = value
		}
	}
	s.agents.Store(&next)
}

// BoundAgents lists the agents that may use the proxy, sorted.
func (s *Server) BoundAgents() []string {
	names := []string{}
	if current := s.agents.Load(); current != nil {
		for name := range *current {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// authenticate finds the profile an agent's proxy credentials give it.
func (s *Server) authenticate(policy *Policy, agent, token string) *Profile {
	current := s.agents.Load()
	if current == nil {
		return nil
	}
	binding, ok := (*current)[agent]
	if !ok || binding.Token == "" || subtle.ConstantTimeCompare([]byte(binding.Token), []byte(token)) != 1 {
		return nil
	}
	profile, ok := policy.Profiles[binding.Profile]
	if !ok {
		return nil
	}
	return &profile
}

// PolicyHash is the hash of the loaded policy, or empty when none is loaded.
func (s *Server) PolicyHash() string {
	if policy := s.policy.Load(); policy != nil {
		return policy.Hash
	}
	return ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "egzo proxy supports CONNECT only", http.StatusMethodNotAllowed)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(host)

	policy := s.policy.Load()
	if policy == nil {
		// Not the agent's fault, and no use asking for credentials: the proxy lost its policy (it
		// lives in memory) and is waiting for `egzo up` to load it again.
		s.audit.Log(Event{Host: host, Action: "deny", Reason: "no egress policy loaded: run `egzo up`"})
		http.Error(w, "egzo: this proxy has no egress policy loaded (it was restarted): run `egzo up`", http.StatusServiceUnavailable)
		return
	}
	agent, token, hasCredentials := proxyCredentials(r)
	var agentPolicy *Profile
	if hasCredentials {
		agentPolicy = s.authenticate(policy, agent, token)
	}
	if agentPolicy == nil {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: "missing or invalid proxy credentials"})
		w.Header().Set("Proxy-Authenticate", `Basic realm="egzo"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}

	if port != allowedPort {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: "only port 443 is allowed"})
		http.Error(w, "only port 443 is allowed", http.StatusForbidden)
		return
	}
	decision := agentPolicy.Decide(host)
	if !decision.Allowed {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: decision.Reason})
		http.Error(w, "egzo: "+host+" is not allowed by this agent's egress profile", http.StatusForbidden)
		return
	}
	if ip := net.ParseIP(host); ip != nil && !isPublic(ip) {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: "not a public address"})
		http.Error(w, "egzo: "+host+" is not a public address", http.StatusForbidden)
		return
	}
	release, reason := s.limits.acquire(agent, s.MaxPerAgent, s.MaxTotal)
	if release == nil {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: reason})
		http.Error(w, "egzo: "+reason, http.StatusServiceUnavailable)
		return
	}
	defer release()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack connection", http.StatusInternalServerError)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		conn.Close()
		return
	}
	// A client may send its first TLS bytes without waiting for the 200: the HTTP server may already
	// have read them, and they belong to the tunnel.
	if n := buffered.Reader.Buffered(); n > 0 {
		pending, _ := buffered.Reader.Peek(n)
		conn = &prefixedConn{Conn: conn, pending: append([]byte(nil), pending...)}
	}

	if decision.Inject != nil || decision.Inspect || len(decision.Substitutions) > 0 {
		s.intercept(conn, agent, host, decision)
		return
	}
	s.tunnel(conn, agent, host)
}

// proxyCredentials reads the agent name and token from Proxy-Authorization.
func proxyCredentials(r *http.Request) (agent, token string, ok bool) {
	header := r.Header.Get("Proxy-Authorization")
	scheme, encoded, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Basic") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	agent, token, ok = strings.Cut(string(decoded), ":")
	return agent, token, ok
}

// tunnel connects the agent straight to the host: the proxy sees the host name, never the traffic.
func (s *Server) tunnel(client net.Conn, agent, host string) {
	defer client.Close()

	// The tunnel was approved for host: the TLS server name must say the same, or an allowed name
	// could be used to reach a different server (domain fronting).
	started := time.Now()
	hello, serverName, err := readClientHello(client)
	switch {
	case err != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)):
		s.audit.Log(Event{Agent: agent, Host: host, Action: "aborted", Reason: "client closed before sending a ClientHello"})
		return
	case err != nil:
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: err.Error()})
		return
	case !strings.EqualFold(serverName, host):
		s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: "TLS server name " + quoteName(serverName) + " differs from the approved host"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	upstream, err := s.Dial(ctx, "tcp", net.JoinHostPort(host, allowedPort))
	if err != nil {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "error", Reason: "dial: " + err.Error()})
		return
	}
	defer upstream.Close()
	if _, err := upstream.Write(hello); err != nil {
		s.audit.Log(Event{Agent: agent, Host: host, Action: "error", Reason: "write: " + err.Error()})
		return
	}
	s.audit.Log(Event{Agent: agent, Host: host, Action: "allow"})

	var wg sync.WaitGroup
	var up, down int64
	wg.Add(2)
	pump := func(dst, src net.Conn, counted *int64) {
		defer wg.Done()
		*counted = copyIdle(dst, src, s.IdleTimeout)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			closer.CloseWrite()
		}
	}
	up = int64(len(hello))
	var sent int64
	go pump(upstream, client, &sent)
	go pump(client, upstream, &down)
	wg.Wait()
	s.audit.Log(Event{Agent: agent, Host: host, Action: "close", BytesUp: up + sent, BytesDown: down, DurationMs: time.Since(started).Milliseconds()})
}

// prefixedConn reads bytes that were already taken off the connection before the rest.
type prefixedConn struct {
	net.Conn
	pending []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (c *prefixedConn) CloseWrite() error {
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return nil
}

// intercept terminates the agent's TLS with a certificate from the project CA so the proxy can add
// the credential, then forwards each request to the real host. Toward the agent it always speaks
// HTTP/1.1, whatever the upstream speaks.
func (s *Server) intercept(client net.Conn, agent, host string, decision Decision) {
	tlsConn := tls.Server(idleConn{client, s.IdleTimeout}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" && !strings.EqualFold(hello.ServerName, host) {
				return nil, errors.New("TLS server name does not match the CONNECT target")
			}
			return s.minter.Certificate(host)
		},
	})
	client.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		// Clients such as Claude Code open connections ahead of time and drop them: not a denial.
		action := "error"
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") {
			action = "aborted"
		}
		s.audit.Log(Event{Agent: agent, Host: host, Action: action, Reason: "tls handshake: " + err.Error()})
		client.Close()
		return
	}
	client.SetDeadline(time.Time{})

	action := "inspect"
	if decision.Inject != nil || len(decision.Substitutions) > 0 {
		action = "inject"
	}
	s.audit.Log(Event{Agent: agent, Host: host, Action: action})

	listener := newConnListener(tlsConn)
	server := &http.Server{
		Handler:           s.interceptHandler(agent, host, decision.Inject, newSwapper(decision.Substitutions)),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       s.IdleTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	server.Serve(listener)
}

func (s *Server) interceptHandler(agent, host string, inject *Injection, swap *swapper) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = host
			r.Out.Host = host
			r.Out.Header.Del("Proxy-Authorization")
			if inject != nil {
				r.Out.Header.Set(inject.Header, inject.Value)
			}
		},
		Transport:     s.Transport,
		FlushInterval: -1, // stream responses, such as model output, as they arrive
		ModifyResponse: func(response *http.Response) error {
			s.audit.Log(Event{Agent: agent, Host: host, Action: "request", Method: response.Request.Method, Path: loggedPath(response.Request), Status: response.StatusCode})
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.audit.Log(Event{Agent: agent, Host: host, Action: "error", Method: r.Method, Path: loggedPath(r), Reason: err.Error()})
			http.Error(w, "egzo: upstream error", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Requests inside the tunnel may only go to the host the tunnel was approved for.
		requested := r.Host
		if h, _, err := net.SplitHostPort(requested); err == nil {
			requested = h
		}
		if !strings.EqualFold(requested, host) {
			s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Reason: "request host " + requested + " differs from the approved tunnel host"})
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		if swap != nil {
			// The path as the agent wrote it: the one that is logged, never the one with a secret in it.
			path := r.URL.Path
			r = r.WithContext(context.WithValue(r.Context(), agentPathKey{}, path))
			if status, reason := swap.apply(r); status != 0 {
				s.audit.Log(Event{Agent: agent, Host: host, Action: "deny", Method: r.Method, Path: path, Reason: reason})
				http.Error(w, "egzo: "+reason, status)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
}

type agentPathKey struct{}

// loggedPath is the path to record for r: the one the agent sent, before any placeholder was swapped.
func loggedPath(r *http.Request) string {
	if path, ok := r.Context().Value(agentPathKey{}).(string); ok {
		return path
	}
	return r.URL.Path
}

// connListener serves exactly one connection with http.Server, then reports closed once that
// connection ends.
type connListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newConnListener(conn net.Conn) *connListener {
	l := &connListener{done: make(chan struct{})}
	l.conn = &notifyConn{Conn: conn, done: l.done}
	return l
}

func (l *connListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *connListener) Close() error   { return nil }
func (l *connListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *notifyConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func quoteName(name string) string {
	if name == "" {
		return "(none)"
	}
	return `"` + name + `"`
}
