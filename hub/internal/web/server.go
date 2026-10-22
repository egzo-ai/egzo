// Package web is the hub's HTTP side: login, the JSON API under /api/v1, the terminal websocket, and the single-page app (built in
// hub/web, embedded here) that is served for every address that is not the API.
package web

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
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

// Authenticator is the login provider.
type Authenticator interface {
	AuthCodeURL(ctx context.Context, redirect, state, nonce string) (string, error)
	Complete(ctx context.Context, redirect, code, nonce string) (auth.Claims, error)
}

// Server is the hub's web application.
type Server struct {
	hub     *hub.Hub
	auth    Authenticator
	urls    *publicurl.Resolver
	log     *slog.Logger
	static  fs.FS
	session time.Duration

	pendingMu sync.Mutex
	pending   map[string]*pendingEngine
}

const (
	sessionCookie = "egzo_session"
	loginCookie   = "egzo_login"
)

// New builds the server.
func New(h *hub.Hub, a Authenticator, urls *publicurl.Resolver, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{hub: h, auth: a, urls: urls, log: log, session: 12 * time.Hour, pending: map[string]*pendingEngine{}}
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

	api := func(pattern string, h authedHandler) { mux.HandleFunc(pattern, s.authed(h)) }
	api("GET "+apiPrefix+"/session", s.whoami)
	mux.HandleFunc("POST "+apiPrefix+"/logout", s.logout)
	api("GET "+apiPrefix+"/projects", s.projects)
	api("GET "+apiPrefix+"/engines", s.operatorOnly(s.listEngines))
	api("POST "+apiPrefix+"/engines", s.operatorOnly(s.saveEngine))
	api("POST "+apiPrefix+"/engines/confirm", s.operatorOnly(s.confirmEngine))
	api("DELETE "+apiPrefix+"/engines/{name}", s.operatorOnly(s.removeEngine))
	api("GET "+apiPrefix+"/users", s.operatorOnly(s.listUsers))
	api("PUT "+apiPrefix+"/users/{id}", s.operatorOnly(s.setOperator))

	const project = apiPrefix + "/e/{engine}/p/{project}"
	api("GET "+project, s.project)
	api("GET "+project+"/events", s.events)
	api("POST "+project+"/spawn", s.spawn)
	api("GET "+project+"/spec", s.spec)
	api("GET "+project+"/members", s.members)
	api("POST "+project+"/members", s.setRole)
	api("GET "+project+"/audit", s.audit)
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
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")
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

// --- login -------------------------------------------------------------------------------------------------------------------

func (s *Server) cookie(r *http.Request, name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.urls.Secure(r), MaxAge: int(ttl.Seconds())}
}

// safeNext only lets a path of this hub through: a login link must not become an open redirect. Control characters are refused
// as well as backslashes: browsers drop tabs and newlines from a URL, so "/<tab>/evil.example" would be "//evil.example" to them.
func safeNext(next string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.Contains(next, "\\") || strings.IndexFunc(next, unicode.IsControl) >= 0 {
		return "/"
	}
	return next
}

func (s *Server) loginStart(w http.ResponseWriter, r *http.Request) {
	state, nonce := auth.Random(), auth.Random()
	redirect := s.urls.Base(r) + "/auth/callback"
	target, err := s.auth.AuthCodeURL(r.Context(), redirect, state, nonce)
	if err != nil {
		s.log.Error("login", "err", err)
		s.loginFail(w, r, err.Error())
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, url.QueryEscape(state)+"|"+url.QueryEscape(nonce)+"|"+url.QueryEscape(safeNext(r.URL.Query().Get("next"))), 10*time.Minute))
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) loginCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		s.loginFail(w, r, "The login did not start here. Try again.")
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, "", -time.Hour))
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 {
		s.loginFail(w, r, "The login state is damaged. Try again.")
		return
	}
	state, _ := url.QueryUnescape(parts[0])
	nonce, _ := url.QueryUnescape(parts[1])
	next, _ := url.QueryUnescape(parts[2])
	if r.URL.Query().Get("state") != state || state == "" {
		s.loginFail(w, r, "The login state does not match. Try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.loginFail(w, r, "The login provider refused: "+e)
		return
	}
	claims, err := s.auth.Complete(r.Context(), s.urls.Base(r)+"/auth/callback", r.URL.Query().Get("code"), nonce)
	if err != nil {
		s.log.Warn("login failed", "err", err)
		s.loginFail(w, r, "Login failed: "+err.Error())
		return
	}
	user, err := s.hub.Login(r.Context(), claims.Identity, claims.Email, claims.Name)
	if err != nil {
		s.loginFail(w, r, err.Error())
		return
	}
	token, err := s.hub.Store().NewSession(r.Context(), user.ID, s.session)
	if err != nil {
		s.loginFail(w, r, err.Error())
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, token, s.session))
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// loginFail sends the browser back to the sign-in screen with what went wrong; the app shows it as text.
func (s *Server) loginFail(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(message), http.StatusSeeOther)
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
