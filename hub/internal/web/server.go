// Package web is the hub's HTTP side: login, the JSON API under /api/v1, the terminal websocket, and the single-page app (built in
// hub/web, embedded here) that is served for every address that is not the API.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

//go:embed all:dist
var distFiles embed.FS

// Authenticator is the single sign-on provider.
type Authenticator interface {
	AuthCodeURL(ctx context.Context, redirect, state, nonce, verifier string) (string, error)
	Complete(ctx context.Context, redirect, code, nonce, verifier string) (auth.Claims, error)
}

// Options are the parts of the sign-in the server shows.
type Options struct {
	// SSOName is what the single sign-on button says ("SSO" when empty).
	SSOName string
}

// Server is the hub's web application.
type Server struct {
	hub     *hub.Hub
	auth    Authenticator // nil without single sign-on
	opts    Options
	urls    *publicurl.Resolver
	log     *slog.Logger
	static  fs.FS
	session time.Duration
	limit   *throttle

	pendingMu sync.Mutex
	pending   map[string]*pendingEngine

	termMu    sync.Mutex
	terminals map[string]map[*int]context.CancelFunc // open terminals by the hash of the session that opened them
}

const (
	sessionCookie = "egzo_session"
	loginCookie   = "egzo_login"
)

// New builds the server. a is nil without single sign-on. The hub's sign-in must be set up first (hub.SetupSignIn).
func New(h *hub.Hub, a Authenticator, opts Options, urls *publicurl.Resolver, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.SSOName == "" {
		opts.SSOName = "SSO"
	}
	s := &Server{hub: h, auth: a, opts: opts, urls: urls, log: log, session: 12 * time.Hour, limit: newThrottle(),
		pending: map[string]*pendingEngine{}, terminals: map[string]map[*int]context.CancelFunc{}}
	var err error
	if s.static, err = fs.Sub(distFiles, "dist"); err != nil {
		return nil, err
	}
	return s, nil
}

// UseApp replaces the embedded app with another file system (index.html and assets/...), for tests of the server.
func (s *Server) UseApp(fsys fs.FS) { s.static = fsys }

// Handler is the whole application.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /login/start", s.loginStart)
	mux.HandleFunc("GET /auth/callback", s.loginCallback)
	mux.HandleFunc("POST /auth/login", s.passwordLogin)
	mux.HandleFunc("GET "+apiPrefix+"/signin", s.signInOptions)

	api := func(pattern string, h authedHandler) { mux.HandleFunc(pattern, s.authed(h)) }
	api("GET "+apiPrefix+"/session", s.whoami)
	mux.HandleFunc("POST "+apiPrefix+"/logout", s.logout)
	api("POST "+apiPrefix+"/me/link", s.linkStart)
	api("DELETE "+apiPrefix+"/me/identities/{id}", s.unlink)
	api("GET "+apiPrefix+"/projects", s.projects)
	api("GET "+apiPrefix+"/engines", s.operatorOnly(s.listEngines))
	api("POST "+apiPrefix+"/engines", s.operatorOnly(s.saveEngine))
	api("POST "+apiPrefix+"/engines/confirm", s.operatorOnly(s.confirmEngine))
	api("DELETE "+apiPrefix+"/engines/{name}", s.operatorOnly(s.removeEngine))
	api("GET "+apiPrefix+"/users", s.operatorOnly(s.listUsers))
	api("PUT "+apiPrefix+"/users/{id}", s.operatorOnly(s.updateUser))
	api("DELETE "+apiPrefix+"/users/{id}/identities/{identity}", s.operatorOnly(s.removeIdentity))

	const project = apiPrefix + "/e/{engine}/p/{project}"
	api("GET "+project, s.project)
	api("GET "+project+"/events", s.events)
	api("POST "+project+"/spawn", s.spawn)
	api("GET "+project+"/tasks", s.tasks)
	api("GET "+project+"/tasks/{id}", s.task)
	api("DELETE "+project+"/tasks/{id}", s.dismissTask)
	api("GET "+project+"/spec", s.spec)
	api("GET "+project+"/members", s.members)
	api("POST "+project+"/members", s.setRole)
	api("GET "+project+"/audit", s.audit)
	api("GET "+project+"/network", s.network)
	api("GET "+project+"/a/{agent}", s.agent)
	api("POST "+project+"/a/{agent}/{verb}", s.operate)
	api("GET "+project+"/a/{agent}/logs", s.logs)
	api("GET "+project+"/a/{agent}/terminal", s.terminal)
	app := s.appHandler()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, apiPrefix+"/"):
			s.apiFail(w, http.StatusNotFound, "not_found", "There is no such API call.")
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			app.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return s.sameOrigin(s.jsonOnly(securityHeaders(mux)))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data: https:; font-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a state-changing request that does not come from the hub's own pages: the session cookie is SameSite=Lax,
// and this is the second wall.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if changesState(r.Method) {
			origin := r.Header.Get("Origin")
			if origin == "" || origin != s.urls.Base(r) {
				s.apiFail(w, http.StatusForbidden, "forbidden", "Cross-site request refused.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// appHandler serves the built app: a file of it when the address names one, and index.html for every other address, so the app's own
// routes (/e/lab/p/web, /engines) survive a reload. Built files carry a hash in their name and may be cached for good.
func (s *Server) appHandler() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if info, err := fs.Stat(s.static, name); name != "" && err == nil && !info.IsDir() && name != "index.html" {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(s.static, "index.html")
		if err != nil {
			http.Error(w, "the web app is not built into this binary (make hub-web)", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(index)
	})
}

// --- login ---------------------------------------------------------------------------------------------------------------------

func (s *Server) cookie(r *http.Request, name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.urls.Secure(r), MaxAge: int(ttl.Seconds())}
}

// safeNext only lets a path of this hub through: a login link must not become an open redirect. Control characters are refused
// as well as backslashes: browsers drop tabs and newlines from a URL, so "/<tab>/evil.example" would be "//evil.example" to them.
func safeNext(next string) string {
	if len(next) > 1 && next[0] == '/' && next[1] != '/' && next[1] != '\\' &&
		!strings.Contains(next, "\\") && strings.IndexFunc(next, unicode.IsControl) < 0 {
		if u, err := url.Parse(next); err == nil && u.Scheme == "" && u.Host == "" {
			return next
		}
	}
	return "/"
}

// signInOptions tells the sign-in page which ways in exist; it needs no session.
func (s *Server) signInOptions(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"local": s.hub.SignIn().LocalLogin, "sso": ""}
	if s.auth != nil {
		out["sso"] = s.opts.SSOName
		out["redirect"] = s.urls.Base(r) + "/auth/callback"
	}
	writeJSON(w, http.StatusOK, out)
}

// passwordLogin signs in with the starter account's username and password. Every failure answers the same, and an account that
// failed several times in a row waits longer and longer before the next try.
func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		s.apiFail(w, http.StatusUnsupportedMediaType, "bad_request", "Send application/json.")
		return
	}
	var body struct{ Username, Password string }
	if !decode(w, r, &body) {
		return
	}
	if !s.hub.SignIn().LocalLogin {
		s.apiFail(w, http.StatusNotFound, "not_found", "Signing in with a password is turned off.")
		return
	}
	key := strings.ToLower(strings.TrimSpace(body.Username))
	if wait := s.limit.try(key); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+1)))
		s.apiFail(w, http.StatusTooManyRequests, "too_many", fmt.Sprintf("Too many failed sign-ins. Try again in %d seconds.", int(wait.Seconds()+1)))
		return
	}
	user, err := s.hub.PasswordLogin(r.Context(), strings.TrimSpace(body.Username), body.Password)
	if err != nil {
		if errors.Is(err, hub.ErrBadLogin) {
			s.apiFail(w, http.StatusUnauthorized, "bad_login", "Wrong username or password.")
			return
		}
		s.problem(w, r, err)
		return
	}
	s.limit.reset(key)
	token, err := s.hub.Store().NewSession(r.Context(), user.ID, store.MethodPassword, "", s.session)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, token, s.session))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// loginState is what the login cookie keeps between the redirect to the provider and the callback. link is the user an identity
// is being linked to; empty for a sign-in.
type loginState struct {
	state, nonce, verifier, next, link string
}

func (l loginState) encode() string {
	parts := []string{l.state, l.nonce, l.verifier, l.next, l.link}
	for i := range parts {
		parts[i] = url.QueryEscape(parts[i])
	}
	return strings.Join(parts, "|")
}

func decodeLoginState(v string) (loginState, bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 5 {
		return loginState{}, false
	}
	for i := range parts {
		parts[i], _ = url.QueryUnescape(parts[i])
	}
	return loginState{state: parts[0], nonce: parts[1], verifier: parts[2], next: parts[3], link: parts[4]}, true
}

// providerURL is where to send the browser to sign in at the provider (or link an account there), with the login cookie set.
func (s *Server) providerURL(w http.ResponseWriter, r *http.Request, next, link string) (string, error) {
	l := loginState{state: auth.Random(), nonce: auth.Random(), verifier: auth.NewVerifier(), next: safeNext(next), link: link}
	target, err := s.auth.AuthCodeURL(r.Context(), s.urls.Base(r)+"/auth/callback", l.state, l.nonce, l.verifier)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, s.cookie(r, loginCookie, l.encode(), 10*time.Minute))
	return target, nil
}

func (s *Server) loginStart(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.loginFail(w, r, "Single sign-on is not configured.")
		return
	}
	target, err := s.providerURL(w, r, r.URL.Query().Get("next"), "")
	if err != nil {
		s.log.Error("login", "err", err)
		s.loginFail(w, r, err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// linkStart begins linking an identity at the provider to the signed-in user. It is a POST, so no other page can start it for
// them; the app sends the browser to the address it answers.
func (s *Server) linkStart(w http.ResponseWriter, r *http.Request, user store.User) {
	if s.auth == nil {
		s.apiFail(w, http.StatusNotFound, "not_found", "Single sign-on is not configured.")
		return
	}
	target, err := s.providerURL(w, r, "/settings/account", user.ID)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": target})
}

func (s *Server) loginCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	if err != nil || s.auth == nil {
		s.loginFail(w, r, "The login did not start here. Try again.")
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, "", -time.Hour))
	l, ok := decodeLoginState(c.Value)
	if !ok {
		s.loginFail(w, r, "The login state is damaged. Try again.")
		return
	}
	if r.URL.Query().Get("state") != l.state || l.state == "" {
		s.loginFail(w, r, "The login state does not match. Try again.")
		return
	}
	fail := s.loginFail
	if l.link != "" {
		fail = func(w http.ResponseWriter, r *http.Request, message string) {
			http.Redirect(w, r, "/settings/account?error="+url.QueryEscape(message), http.StatusSeeOther)
		}
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(w, r, "The login provider refused: "+e)
		return
	}
	claims, err := s.auth.Complete(r.Context(), s.urls.Base(r)+"/auth/callback", r.URL.Query().Get("code"), l.nonce, l.verifier)
	if err != nil {
		s.log.Warn("login failed", "err", err)
		fail(w, r, "Login failed: "+err.Error())
		return
	}
	profile := store.Profile{Email: claims.Email, EmailVerified: claims.EmailVerified, Name: claims.Name, PreferredUsername: claims.PreferredUsername, Picture: claims.Picture}
	if l.link != "" {
		s.linkCallback(w, r, l, claims, profile, fail)
		return
	}
	user, identityID, err := s.hub.IdentityLogin(r.Context(), claims.Issuer, claims.Subject, profile)
	if err != nil {
		fail(w, r, capitalize(err.Error())+".")
		return
	}
	token, err := s.hub.Store().NewSession(r.Context(), user.ID, store.MethodIdentity, identityID, s.session)
	if err != nil {
		fail(w, r, err.Error())
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, token, s.session))
	http.Redirect(w, r, safeNext(l.next), http.StatusSeeOther)
}

// linkCallback adds the identity to the user who started the link, if they are still the one signed in. No session is made.
func (s *Server) linkCallback(w http.ResponseWriter, r *http.Request, l loginState, claims auth.Claims, profile store.Profile, fail func(http.ResponseWriter, *http.Request, string)) {
	var user store.User
	c, err := r.Cookie(sessionCookie)
	if err == nil {
		user, err = s.hub.Store().SessionUser(r.Context(), c.Value)
	}
	if err != nil || user.ID != l.link {
		s.loginFail(w, r, "The link was started by somebody who is no longer signed in here. Sign in and try again.")
		return
	}
	if _, err := s.hub.LinkIdentity(r.Context(), user, claims.Issuer, claims.Subject, profile); err != nil {
		var taken *store.IdentityTakenError
		switch {
		case errors.As(err, &taken):
			fail(w, r, "That account at the provider already belongs to "+taken.Username+". An operator can disable that user and remove the account from it.")
		case errors.Is(err, hub.ErrIssuerLinked):
			fail(w, r, "You already have an account at this provider linked. Unlink it first.")
		default:
			fail(w, r, "Linking failed: "+err.Error())
		}
		return
	}
	http.Redirect(w, r, "/settings/account?linked=1", http.StatusSeeOther)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// loginFail sends the browser back to the sign-in screen with what went wrong; the app shows it as text.
func (s *Server) loginFail(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// --- terminals of a session --------------------------------------------------------------------------------------------------

// trackTerminal registers an open terminal under the session that opened it; the returned func forgets it.
func (s *Server) trackTerminal(sessionHash string, cancel context.CancelFunc) func() {
	key := new(int)
	s.termMu.Lock()
	if s.terminals[sessionHash] == nil {
		s.terminals[sessionHash] = map[*int]context.CancelFunc{}
	}
	s.terminals[sessionHash][key] = cancel
	s.termMu.Unlock()
	return func() {
		s.termMu.Lock()
		delete(s.terminals[sessionHash], key)
		if len(s.terminals[sessionHash]) == 0 {
			delete(s.terminals, sessionHash)
		}
		s.termMu.Unlock()
	}
}

// endTerminals closes the terminals of sessions that ended: a session's end is the end of what it opened.
func (s *Server) endTerminals(sessionHashes []string) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	for _, h := range sessionHashes {
		for _, cancel := range s.terminals[h] {
			cancel()
		}
	}
}

type authedHandler func(w http.ResponseWriter, r *http.Request, user store.User)

func (s *Server) authed(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		var user store.User
		if err == nil {
			user, err = s.hub.Store().SessionUser(r.Context(), c.Value)
		}
		if err != nil {
			s.apiFail(w, http.StatusUnauthorized, "unauthorized", "Sign in again.")
			return
		}
		next(w, r, user)
	}
}
