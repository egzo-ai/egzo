// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/preview"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// previewCookie is what a preview's host keeps: it says nothing, and stands for a row of preview_sessions.
const previewCookie = "egzo_preview"

// previewStart is where a preview sends a person with no cookie for it. The hub's own cookie reaches this, and never reaches a preview: a sign-in
// crosses over as a ticket, in a redirect.
func (s *Server) previewStart(w http.ResponseWriter, r *http.Request) {
	host, ok := preview.ParseHost(r.PathValue("host"), s.opts.PreviewDomain)
	if !ok {
		preview.Page(w, http.StatusNotFound, "There is no preview at this address.")
		return
	}
	var user store.User
	c, err := s.readCookie(r, sessionCookie)
	if err == nil {
		user, err = s.hub.Store().SessionUser(r.Context(), c.Value)
	}
	if err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	ticket, err := s.hub.OpenPreview(r.Context(), user, store.HashToken(c.Value), host.Project, host.ID, safeNext(r.URL.Query().Get("to")))
	if errors.Is(err, hub.ErrNotFound) {
		preview.Page(w, http.StatusNotFound, "There is no preview at this address, or it is not yours to open.")
		return
	}
	if err != nil {
		s.problem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.opts.PreviewScheme+"://"+host.Name+"/_egzo/enter?ticket="+url.QueryEscape(ticket), http.StatusSeeOther)
}

// PreviewGate is who passes on the preview listener.
func (s *Server) PreviewGate() preview.Gate { return previewGate{s} }

type previewGate struct{ s *Server }

// cookieName is the cookie's name on a preview's host. Over https another host cannot plant or overwrite it, and every preview is another host.
func (g previewGate) cookieName() string {
	if g.s.opts.PreviewScheme == "https" {
		return hostOnly + previewCookie
	}
	return previewCookie
}

// Enter spends a ticket and sets the cookie. The ticket has done its work once the browser leaves this address.
func (g previewGate) Enter(w http.ResponseWriter, r *http.Request, host preview.Host) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/_egzo/enter" || r.Method != http.MethodGet {
		preview.Page(w, http.StatusNotFound, "There is nothing here.")
		return
	}
	path, token, err := g.s.hub.EnterPreview(r.Context(), r.URL.Query().Get("ticket"), host.Project, host.ID)
	if err != nil {
		preview.Page(w, http.StatusForbidden, "This link was used already, or it expired. Open the preview's address again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: g.cookieName(), Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: g.s.opts.PreviewScheme == "https", MaxAge: int(g.s.session.Seconds())})
	http.Redirect(w, r, safeNext(path), http.StatusSeeOther)
}

// Allow checks every request again. A page sent away keeps what it was asking for; what cannot follow a redirect is refused.
func (g previewGate) Allow(w http.ResponseWriter, r *http.Request, host preview.Host) (context.Context, bool) {
	var visitor hub.Visitor
	c, err := r.Cookie(g.cookieName())
	if err == nil {
		visitor, err = g.s.hub.PreviewVisitor(r.Context(), c.Value, host.Project, host.ID)
	}
	if err != nil {
		if r.Method != http.MethodGet && r.Method != http.MethodHead || r.Header.Get("Upgrade") != "" {
			preview.Page(w, http.StatusUnauthorized, "Sign in again: reload the page.")
			return nil, false
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, strings.TrimRight(g.s.urls.Base(r), "/")+"/preview/"+host.Name+"?to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return nil, false
	}
	// What stays open past this check ends like a terminal: with its session, its role, or the port.
	ctx, cancel := context.WithDeadline(r.Context(), visitor.Expires)
	forget := g.s.trackTerminal(visitor.Session, cancel)
	stop := g.s.hub.TrackPreview(host.Project, host.ID, visitor.User.ID, cancel)
	context.AfterFunc(ctx, func() { forget(); stop(); cancel() })
	return ctx, true
}

// StripRequest takes the cookie out of what the app sees. The rest of the cookies, and Authorization, are the app's.
func (g previewGate) StripRequest(header http.Header) {
	var kept []string
	for _, line := range header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if name != g.cookieName() {
				kept = append(kept, strings.TrimSpace(pair))
			}
		}
	}
	header.Del("Cookie")
	if len(kept) > 0 {
		header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// StripResponse stops an app from setting the cookie: it could replace a person's with its own.
func (g previewGate) StripResponse(header http.Header) {
	var kept []string
	for _, line := range header.Values("Set-Cookie") {
		name, _, _ := strings.Cut(line, "=")
		if strings.TrimSpace(name) != g.cookieName() {
			kept = append(kept, line)
		}
	}
	header.Del("Set-Cookie")
	for _, line := range kept {
		header.Add("Set-Cookie", line)
	}
}
