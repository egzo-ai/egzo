// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/hublink"
)

const (
	// maxHandshakes is how many links may be proving their key at once. Anyone who reaches the endpoint may try, and until the proof is
	// done nothing says who they are.
	maxHandshakes = 64
	// handshakeLimit is how long a link gets to open its control stream and prove its key: three lines. A link asks its engine what to
	// say before it dials.
	handshakeLimit = 10 * time.Second
)

// link takes one link's connection: its WebSocket, its proof, and then the hub holds it until it ends. A proof that does not verify is
// refused here, so that what reaches the hub is a key somebody holds. All that a stranger can reach is this, so what one address may
// try is limited (gate.go); a project that is registered and connected counts for nothing.
func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	client := s.urls.ClientIP(r)
	if !s.gate.enter(client) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many attempts from this address: try again", http.StatusTooManyRequests)
		return
	}
	var left sync.Once
	leave := func() { left.Do(func() { s.gate.leave(client) }) }
	defer leave()
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
		if s.gate.failed(client) {
			s.log.Warn("a link was refused: does its hub: say "+s.opts.LinkURL+"?", "err", err, "from", client)
		}
		return
	}
	proved()
	leave()
	// the connection is hijacked, and r.Context() knows nothing of its end: Run does
	err = s.hub.Link(context.Background(), peer.Hello, peer.Code, peer)
	switch {
	case errors.Is(err, hub.ErrUnregistered), errors.Is(err, hub.ErrBadCode), errors.Is(err, hub.ErrBadHello):
		if s.gate.failed(client) {
			s.log.Info("a link that is not registered was turned away", "err", err, "from", client)
		}
	case err != nil && !errors.Is(err, context.Canceled):
		s.log.Debug("a link went away", "project", peer.Hello.Project, "err", err)
	}
}

// unlinkProject is an owner's destroy: the hub forgets the project, which keeps running on its machine.
func (s *Server) unlinkProject(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.Unlink(r.Context(), user, r.PathValue("project")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// linkJSON is a registration, for an operator.
type linkJSON struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Host         string     `json:"host"`
	Release      string     `json:"release"`
	Templates    []string   `json:"templates"`
	State        string     `json:"state"`           // approved or revoked
	Owner        string     `json:"owner,omitempty"` // who connected it
	Owners       []string   `json:"owners"`          // who can answer for it now: none on an approved project is for the operators to deal with
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	OfflineSince *time.Time `json:"offlineSince"`
}

func linkOf(v hub.LinkView) linkJSON {
	r := v.Registration
	j := linkJSON{ID: r.ID, Name: r.Name, Host: r.Host, Release: r.Release, Templates: r.Templates, State: r.State, Owner: v.OwnerName, Owners: v.Owners, Message: r.Message,
		CreatedAt: r.Created, OfflineSince: offlineOf(r.Offline)}
	if j.Templates == nil {
		j.Templates = []string{}
	}
	if j.Owners == nil {
		j.Owners = []string{}
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
		rows = append(rows, linkOf(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": rows})
}

// linkCodeJSON is a link code as it is listed: the code itself is shown when it is made, and never again.
type linkCodeJSON struct {
	ID        string    `json:"id"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Server) createLinkCode(w http.ResponseWriter, r *http.Request, user store.User) {
	made, code, err := s.hub.CreateLinkCode(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": made.ID, "code": code, "expiresAt": made.Expires, "hub": s.opts.LinkURL})
}

func (s *Server) listLinkCodes(w http.ResponseWriter, r *http.Request, user store.User) {
	views, err := s.hub.LinkCodes(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	rows := make([]linkCodeJSON, 0, len(views))
	for _, v := range views {
		rows = append(rows, linkCodeJSON{ID: v.ID, CreatedBy: v.CreatorName, CreatedAt: v.Created, ExpiresAt: v.Expires})
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": rows})
}

func (s *Server) cancelLinkCode(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.CancelLinkCode(r.Context(), user, r.PathValue("id")); err != nil {
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
