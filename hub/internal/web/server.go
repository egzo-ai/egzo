// Package web is the hub's HTTP side: login, the pages, the terminal websocket. Pages are rendered on the server; the browser
// needs scripts only for the terminal and to refresh a status panel.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

//go:embed templates/*.html
var templateFiles embed.FS

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
	pages   map[string]*template.Template
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
	if s.pages, err = parseTemplates(); err != nil {
		return nil, err
	}
	if s.static, err = fs.Sub(distFiles, "dist"); err != nil {
		return nil, err
	}
	return s, nil
}

func parseTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"ago": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%d min ago", int(d.Minutes()))
			case d < 48*time.Hour:
				return fmt.Sprintf("%d h ago", int(d.Hours()))
			}
			return fmt.Sprintf("%d days ago", int(d.Hours()/24))
		},
		"clock": func(t time.Time) string { return t.Local().Format("15:04:05") },
		"stamp": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	}
	layout, err := fs.ReadFile(templateFiles, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(templateFiles, "templates")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".html")
		if name == "layout" {
			continue
		}
		body, err := fs.ReadFile(templateFiles, "templates/"+e.Name())
		if err != nil {
			return nil, err
		}
		t, err := template.New(name).Funcs(funcs).Parse(string(layout))
		if err != nil {
			return nil, err
		}
		if _, err := t.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		pages[name] = t
	}
	return pages, nil
}

// Handler is the whole application.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /login/start", s.loginStart)
	mux.HandleFunc("GET /auth/callback", s.loginCallback)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /{$}", s.authed(s.home))
	mux.HandleFunc("GET /engines", s.authed(s.enginesPage))
	mux.HandleFunc("POST /engines", s.authed(s.saveEngine))
	mux.HandleFunc("POST /engines/confirm", s.authed(s.confirmEngine))
	mux.HandleFunc("POST /engines/{name}/remove", s.authed(s.removeEngine))

	const project = "/e/{engine}/p/{project}"
	mux.HandleFunc("GET "+project, s.authed(s.projectPage))
	mux.HandleFunc("POST "+project+"/spawn", s.authed(s.spawn))
	mux.HandleFunc("GET "+project+"/spec", s.authed(s.specPage))
	mux.HandleFunc("GET "+project+"/members", s.authed(s.membersPage))
	mux.HandleFunc("POST "+project+"/members", s.authed(s.setRole))
	mux.HandleFunc("GET "+project+"/audit", s.authed(s.auditPage))
	mux.HandleFunc("GET "+project+"/a/{agent}", s.authed(s.agentPage))
	mux.HandleFunc("POST "+project+"/a/{agent}/{verb}", s.authed(s.operate))
	mux.HandleFunc("GET "+project+"/a/{agent}/logs", s.authed(s.logsPage))
	mux.HandleFunc("GET "+project+"/a/{agent}/terminal", s.authed(s.terminal))
	return s.sameOrigin(securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a state-changing request that does not come from the hub's own pages: the session cookie is SameSite=Lax,
// and this is the second wall.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin == "" || origin != s.urls.Base(r) {
				s.fail(w, r, http.StatusForbidden, "Cross-site request refused.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// --- login -------------------------------------------------------------------------------------------------------------------

func (s *Server) cookie(r *http.Request, name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.urls.Secure(r), MaxAge: int(ttl.Seconds())}
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "login", map[string]any{"Next": safeNext(r.URL.Query().Get("next"))})
}

// safeNext only lets a path of this hub through: a login link must not become an open redirect.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
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
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, url.QueryEscape(state)+"|"+url.QueryEscape(nonce)+"|"+url.QueryEscape(safeNext(r.URL.Query().Get("next"))), 10*time.Minute))
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) loginCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "The login did not start here. Try again.")
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, "", -time.Hour))
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 {
		s.fail(w, r, http.StatusBadRequest, "The login state is damaged. Try again.")
		return
	}
	state, _ := url.QueryUnescape(parts[0])
	nonce, _ := url.QueryUnescape(parts[1])
	next, _ := url.QueryUnescape(parts[2])
	if r.URL.Query().Get("state") != state || state == "" {
		s.fail(w, r, http.StatusBadRequest, "The login state does not match. Try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.fail(w, r, http.StatusForbidden, "The login provider refused: "+e)
		return
	}
	claims, err := s.auth.Complete(r.Context(), s.urls.Base(r)+"/auth/callback", r.URL.Query().Get("code"), nonce)
	if err != nil {
		s.log.Warn("login failed", "err", err)
		s.fail(w, r, http.StatusForbidden, "Login failed: "+err.Error())
		return
	}
	user, err := s.hub.Login(r.Context(), claims.Identity, claims.Email, claims.Name)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.hub.Store().NewSession(r.Context(), user.ID, s.session)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, token, s.session))
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.hub.Store().EndSession(r.Context(), c.Value)
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, "", -time.Hour))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
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
			if r.Method == http.MethodGet && r.URL.Query().Get("fragment") == "" && !isWebsocket(r) {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "sign in again", http.StatusUnauthorized)
			return
		}
		next(w, r, user)
	}
}

func isWebsocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// --- rendering ---------------------------------------------------------------------------------------------------------------

// page is what every template receives.
type page struct {
	Title    string
	User     *store.User
	Engines  []hub.EngineView
	Multi    bool // more than one engine: badges and the filter are shown
	Flash    string
	Error    string
	Data     any
	Fragment bool
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	p := page{Data: data, Fragment: r.URL.Query().Get("fragment") != ""}
	if m, ok := data.(map[string]any); ok {
		if t, ok := m["Title"].(string); ok {
			p.Title = t
		}
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.hub.Store().SessionUser(r.Context(), c.Value); err == nil {
			p.User = &u
			p.Engines, _ = s.hub.Engines(r.Context())
			p.Multi = len(p.Engines) > 1
		}
	}
	p.Flash = r.URL.Query().Get("ok")
	p.Error = r.URL.Query().Get("err")
	t := s.pages[name]
	if t == nil {
		http.Error(w, "no such page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	entry := "page"
	if p.Fragment {
		entry = "fragment"
	}
	if err := t.ExecuteTemplate(w, entry, p); err != nil {
		s.log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.render(w, r, status, "error", map[string]any{"Title": http.StatusText(status), "Status": status, "Message": message})
}

// problem turns an error of the hub into a page.
func (s *Server) problem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, hub.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "There is nothing here, or you have no access to it.")
	case errors.Is(err, hub.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "You are not allowed to do that.")
	default:
		s.log.Error("request", "path", r.URL.Path, "err", err)
		s.fail(w, r, http.StatusBadGateway, err.Error())
	}
}

// back redirects to a page with a message in the address.
func back(w http.ResponseWriter, r *http.Request, to, key, message string) {
	u, _ := url.Parse(to)
	q := u.Query()
	q.Set(key, message)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
