// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package preview serves what agents expose to people's browsers: a host name of its own per preview, a reverse proxy over the project's link,
// with limits, because the server on the other side is untrusted. It depends on two things only: a Gate that says who may pass, and a
// Backend that finds and reaches a preview. See roadmap/agent-ports.md.
package preview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound is a preview that does not exist, or is not exposed any more.
	ErrNotFound = errors.New("no such preview")
	// ErrOffline is a preview whose project's link is not connected.
	ErrOffline = errors.New("the project is offline")
	errBusy    = errors.New("too many connections")
)

// Target is where a preview is now.
type Target struct {
	Project, ID, Agent string
	Port               int
}

// Backend finds and reaches previews. Dial follows net.Dialer: ctx covers the dial and nothing after it.
type Backend interface {
	Find(ctx context.Context, project, id string) (Target, error)
	Dial(ctx context.Context, target Target) (net.Conn, error)
}

// Gate says who passes. Enter takes what is under /_egzo/, which never reaches the app. Allow answers a request that may not go on, by a
// redirect to sign in or a refusal, and returns false; the context it returns is what the proxied request runs under, so whoever made
// it can end it. Strip removes the gate's own traces from a request on its way to the app, and from an answer on its way back.
type Gate interface {
	Enter(w http.ResponseWriter, r *http.Request, host Host)
	Allow(w http.ResponseWriter, r *http.Request, host Host) (context.Context, bool)
	StripRequest(header http.Header)
	StripResponse(header http.Header)
}

// Options are the limits of the proxy; a zero value is the default.
type Options struct {
	// Domain is what previews live under, with a port if people reach them on one.
	Domain string
	// Scheme is how people reach them.
	Scheme string
	// HostOf says which host a request was for.
	HostOf func(*http.Request) string
	// PerPreview and PerHub are how many connections may be open to the agents' servers (32, 1024).
	PerPreview, PerHub int
	// Queue is how long a request waits for a connection over those limits (10 seconds): a dev server's page asks for hundreds of files at once.
	Queue time.Duration
	// HeaderTime is how long a server has to start answering (60 seconds).
	HeaderTime time.Duration
	// HeaderBytes is how long its answer's headers may be (1 MiB).
	HeaderBytes int64
	// Idle is how long an unused connection is kept (30 seconds): each one is a command running in the agent.
	Idle time.Duration
}

func (o Options) withDefaults() Options {
	if o.PerPreview <= 0 {
		o.PerPreview = 32
	}
	if o.PerHub <= 0 {
		o.PerHub = 1024
	}
	if o.Queue <= 0 {
		o.Queue = 10 * time.Second
	}
	if o.HeaderTime <= 0 {
		o.HeaderTime = time.Minute
	}
	if o.HeaderBytes <= 0 {
		o.HeaderBytes = 1 << 20
	}
	if o.Idle <= 0 {
		o.Idle = 30 * time.Second
	}
	if o.HostOf == nil {
		o.HostOf = func(r *http.Request) string { return r.Host }
	}
	return o
}

// Server serves previews.
type Server struct {
	opts    Options
	backend Backend
	gate    Gate
	proxy   *httputil.ReverseProxy
	limit   *limiter
	log     *slog.Logger
}

// New builds a server; log may be nil.
func New(backend Backend, gate Gate, opts Options, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{opts: opts.withDefaults(), backend: backend, gate: gate, log: log}
	s.limit = newLimiter(s.opts.PerPreview, s.opts.PerHub)
	transport := &http.Transport{
		DialContext:            s.dial,
		DisableCompression:     true, // what the browser and the app agreed on is theirs
		MaxIdleConns:           s.opts.PerHub,
		MaxIdleConnsPerHost:    8,
		IdleConnTimeout:        s.opts.Idle,
		ResponseHeaderTimeout:  s.opts.HeaderTime,
		MaxResponseHeaderBytes: s.opts.HeaderBytes,
	}
	s.proxy = &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		Transport:      transport,
		FlushInterval:  -1, // an event stream goes out as it comes
		ModifyResponse: s.answered,
		ErrorHandler:   s.failed,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	return s
}

// Host is what a preview's host name says.
type Host struct {
	ID, Project string
	Name        string // the whole of it
}

var hostLabel = regexp.MustCompile(`^([a-z2-7]{16})-(p[0-9a-z]+)$`)

// Template is what the addresses of a project look like, with %s for an exposure's id.
func Template(scheme, project, domain string) string {
	return fmt.Sprintf("%s://%%s-%s.%s/", scheme, project, domain)
}

// ParseHost reads a preview's host name. The project is in it, so that no project can claim another's address, and one that is offline is known.
func ParseHost(name, domain string) (Host, bool) {
	name = strings.ToLower(name)
	label, ok := strings.CutSuffix(name, "."+domain)
	if !ok {
		return Host{}, false
	}
	found := hostLabel.FindStringSubmatch(label)
	if found == nil {
		return Host{}, false
	}
	return Host{ID: found[1], Project: found[2], Name: name}, true
}

type requestTarget struct{}

// routed is where a request goes, and the host it came for: the request the proxy makes names another.
type routed struct {
	Target
	host string
}

// ServeHTTP is the preview listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, ok := ParseHost(s.opts.HostOf(r), s.opts.Domain)
	if !ok {
		page(w, http.StatusNotFound, "There is no preview at this address.")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_egzo/") {
		s.gate.Enter(w, r, host)
		return
	}
	if !s.sameOrigin(r, host) {
		page(w, http.StatusForbidden, "Cross-site request refused.")
		return
	}
	ctx, allowed := s.gate.Allow(w, r, host)
	if !allowed {
		return
	}
	target, err := s.backend.Find(ctx, host.Project, host.ID)
	switch {
	case errors.Is(err, ErrOffline):
		page(w, http.StatusServiceUnavailable, "This project is offline: its link to the hub is not connected.")
		return
	case err != nil:
		page(w, http.StatusNotFound, "There is no preview at this address: the agent no longer exposes it.")
		return
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, requestTarget{}, routed{target, host.Name})))
}

// sameOrigin refuses what a page of another origin sends on its own: every preview is on one site, so a cookie does not tell them apart. A
// WebSocket and a method that changes something come with an Origin from any browser.
func (s *Server) sameOrigin(r *http.Request, host Host) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !changesState(r) {
		return true
	}
	return origin == s.opts.Scheme+"://"+host.Name
}

func changesState(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r.Header.Get("Upgrade") != ""
	}
	return true
}

// poolKey keeps one preview's idle connections from serving another. It is a name for the transport, never looked up.
func poolKey(target Target) string {
	return target.ID + "-" + target.Project + ":" + strconv.Itoa(target.Port)
}

func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	target := pr.In.Context().Value(requestTarget{}).(routed)
	pr.SetXForwarded()
	pr.Out.URL.Scheme, pr.Out.URL.Host = "http", poolKey(target.Target)
	pr.Out.Host = "localhost:" + strconv.Itoa(target.Port) // dev servers refuse names they do not know
	pr.Out.Header.Set("X-Forwarded-Host", target.host)
	pr.Out.Header.Set("X-Forwarded-Proto", s.opts.Scheme)
	s.gate.StripRequest(pr.Out.Header)
}

// answered leaves the app's answer as it is, but for the gate's traces: an app knows its address from X-Forwarded-Host, or from what it was told.
func (s *Server) answered(response *http.Response) error {
	s.gate.StripResponse(response.Header)
	return nil
}

// dial reaches a preview, within the limits. The transport keeps connections by address, so the target comes with the request.
func (s *Server) dial(ctx context.Context, _, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	id, project, found := strings.Cut(host, "-")
	if !found {
		return nil, ErrNotFound
	}
	target, err := s.backend.Find(ctx, project, id)
	if err != nil {
		return nil, err
	}
	release, err := s.limit.take(ctx, target.ID, s.opts.Queue)
	if err != nil {
		return nil, err
	}
	conn, err := s.backend.Dial(ctx, target)
	if err != nil {
		release()
		return nil, err
	}
	return &limitedConn{Conn: conn, release: release}, nil
}

// failed says what went wrong in words for the person looking at the page. What a link said is not shown as it is.
func (s *Server) failed(w http.ResponseWriter, r *http.Request, err error) {
	target := r.Context().Value(requestTarget{}).(routed)
	switch {
	case errors.Is(err, context.Canceled):
		return // the person left
	case errors.Is(err, errBusy):
		page(w, http.StatusServiceUnavailable, fmt.Sprintf("Too many connections to this preview, or to the hub's previews: at most %d and %d. Try again.", s.opts.PerPreview, s.opts.PerHub))
	case errors.Is(err, ErrOffline):
		page(w, http.StatusServiceUnavailable, "This project went offline: its link to the hub is not connected.")
	case errors.Is(err, ErrNotFound):
		page(w, http.StatusNotFound, "There is no preview at this address: the agent no longer exposes it.")
	case strings.Contains(err.Error(), "nothing listens on port"):
		page(w, http.StatusBadGateway, fmt.Sprintf("Nothing listens on port %d in %s now.", target.Port, target.Agent))
	case strings.Contains(err.Error(), "is not running"):
		page(w, http.StatusBadGateway, fmt.Sprintf("%s is not running.", target.Agent))
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timeout awaiting response headers"):
		page(w, http.StatusGatewayTimeout, fmt.Sprintf("The server on port %d in %s did not answer in time.", target.Port, target.Agent))
	default:
		s.log.Info("a preview failed", "project", target.Project, "agent", target.Agent, "port", target.Port, "err", err)
		page(w, http.StatusBadGateway, fmt.Sprintf("The server on port %d in %s did not answer as plain HTTP.", target.Port, target.Agent))
	}
}

// page answers in plain text: no page of the hub's runs on a preview's origin.
func page(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintln(w, text)
}

// limiter counts the connections to the agents' servers, per preview and in all. Over a limit it waits, because that is what a page of hundreds
// of files needs, for a while.
type limiter struct {
	perPreview, total int
	mu                sync.Mutex
	open              map[string]int
	all               int
	freed             chan struct{} // closed and replaced when a slot is freed
}

func newLimiter(perPreview, total int) *limiter {
	return &limiter{perPreview: perPreview, total: total, open: map[string]int{}, freed: make(chan struct{})}
}

func (l *limiter) take(ctx context.Context, id string, wait time.Duration) (func(), error) {
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	for {
		l.mu.Lock()
		if l.open[id] < l.perPreview && l.all < l.total {
			l.open[id]++
			l.all++
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.release(id) }) }, nil
		}
		freed := l.freed
		l.mu.Unlock()
		select {
		case <-freed:
		case <-timeout.C:
			return nil, errBusy
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (l *limiter) release(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all--
	if l.open[id]--; l.open[id] <= 0 {
		delete(l.open, id)
	}
	close(l.freed)
	l.freed = make(chan struct{})
}

// limitedConn frees its slot when it closes.
type limitedConn struct {
	net.Conn
	release func()
}

func (c *limitedConn) Close() error {
	defer c.release()
	return c.Conn.Close()
}

// Listener is what serves previews: a request may take as long as an upload takes, which the hub's own server does not allow.
func Listener(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}
