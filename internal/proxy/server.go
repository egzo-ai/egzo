package proxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
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

	// Transport reaches the real upstream for intercepted hosts. Tests replace it.
	Transport http.RoundTripper
	// Dial reaches the real upstream for tunnelled hosts. Tests replace it.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func NewServer(ca *CA, audit *Audit) *Server {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &Server{
		minter: NewMinter(ca),
		audit:  audit,
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

// SetPolicy atomically replaces the policy. Until one is set, everything is denied.
func (s *Server) SetPolicy(policy *Policy) { s.policy.Store(policy) }

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
	agent, token, hasCredentials := proxyCredentials(r)
	var agentPolicy *AgentPolicy
	if policy != nil && hasCredentials {
		agentPolicy, _ = policy.Authenticate(agent, token)
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

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack connection", http.StatusInternalServerError)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		conn.Close()
		return
	}

	if decision.Inject != nil || decision.Inspect {
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
	wg.Add(2)
	pump := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			closer.CloseWrite()
		}
	}
	go pump(upstream, client)
	go pump(client, upstream)
	wg.Wait()
}

// intercept terminates the agent's TLS with a certificate from the project CA so the proxy can add
// the credential, then forwards each request to the real host. Toward the agent it always speaks
// HTTP/1.1, whatever the upstream speaks.
func (s *Server) intercept(client net.Conn, agent, host string, decision Decision) {
	tlsConn := tls.Server(client, &tls.Config{
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
	if decision.Inject != nil {
		action = "inject"
	}
	s.audit.Log(Event{Agent: agent, Host: host, Action: action})

	listener := newConnListener(tlsConn)
	server := &http.Server{
		Handler:           s.interceptHandler(agent, host, decision.Inject),
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	server.Serve(listener)
}

func (s *Server) interceptHandler(agent, host string, inject *Injection) http.Handler {
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
			s.audit.Log(Event{Agent: agent, Host: host, Action: "request", Method: response.Request.Method, Path: response.Request.URL.Path, Status: response.StatusCode})
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.audit.Log(Event{Agent: agent, Host: host, Action: "error", Method: r.Method, Path: r.URL.Path, Reason: err.Error()})
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
		proxy.ServeHTTP(w, r)
	})
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
