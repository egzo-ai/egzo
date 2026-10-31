// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

const (
	// maxHandshakes is how many links may be proving their key at once. Anyone who reaches the endpoint may try, and until the proof is
	// done nothing says who they are.
	maxHandshakes = 64
	// handshakeLimit is how long a link gets to open its control stream and prove its key.
	handshakeLimit = 30 * time.Second
)

// link takes one link's connection: its WebSocket, its proof, and then the hub holds it until it ends. A proof that does not verify is
// refused here, so that what reaches the hub is a key somebody holds.
func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	select {
	case s.greeted <- struct{}{}:
	default:
		http.Error(w, "too many links are connecting: try again", http.StatusServiceUnavailable)
		return
	}
	var once sync.Once
	proved := func() { once.Do(func() { <-s.greeted }) }
	defer proved()
	session, err := hublink.Accept(w, r)
	if err != nil {
		return // Accept has answered: a 426 to what is not a WebSocket
	}
	defer session.Close()
	proving, cancel := context.WithTimeout(context.Background(), handshakeLimit)
	defer cancel()
	peer, err := hublink.Handshake(proving, session, s.opts.LinkURL)
	if err != nil {
		s.log.Warn("a link was refused: does its hub: say "+s.opts.LinkURL+"?", "err", err, "from", r.RemoteAddr)
		return
	}
	proved()
	// the connection is hijacked, and r.Context() knows nothing of its end: Run does
	if err := s.hub.Link(context.Background(), peer.Hello, peer, s.urls.Base(r)); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Debug("a link went away", "project", peer.Hello.Project, "err", err)
	}
}

// linkJSON is a registration, for an operator.
type linkJSON struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Host         string     `json:"host"`
	Release      string     `json:"release"`
	Templates    []string   `json:"templates"`
	State        string     `json:"state"` // pending, approved, refused or revoked
	Code         string     `json:"code,omitempty"`
	Owner        string     `json:"owner,omitempty"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	OfflineSince *time.Time `json:"offlineSince"`
}

func linkOf(r store.Registration, owner string) linkJSON {
	j := linkJSON{ID: r.ID, Name: r.Name, Host: r.Host, Release: r.Release, Templates: r.Templates, State: r.State, Code: r.Code, Owner: owner, Message: r.Message,
		CreatedAt: r.Created, OfflineSince: offlineOf(r.Offline)}
	if j.Templates == nil {
		j.Templates = []string{}
	}
	return j
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request, user store.User) {
	views, err := s.hub.Links(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	rows := make([]linkJSON, 0, len(views))
	for _, v := range views {
		rows = append(rows, linkOf(v.Registration, v.OwnerName))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": rows})
}

// pendingLink is the page that approves a registration: the project behind a device code.
func (s *Server) pendingLink(w http.ResponseWriter, r *http.Request, user store.User) {
	reg, err := s.hub.Pending(r.Context(), user, r.PathValue("code"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]linkJSON{"link": linkOf(reg, "")})
}

// decideLink approves or refuses the project behind a device code.
func (s *Server) decideLink(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		Approve *bool  `json:"approve"`
		Message string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Approve == nil {
		s.apiFail(w, http.StatusBadRequest, "bad_request", "Say approve: true or false.")
		return
	}
	if *body.Approve {
		reg, err := s.hub.Approve(r.Context(), user, r.PathValue("code"))
		if err != nil {
			s.problem(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"project": reg.ID})
		return
	}
	if err := s.hub.Refuse(r.Context(), user, r.PathValue("code"), body.Message); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) revokeLink(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		Message string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.hub.Revoke(r.Context(), user, r.PathValue("id"), body.Message); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) forgetLink(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.Forget(r.Context(), user, r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
