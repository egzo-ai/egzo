// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// A person links their chat account to their hub user on the page a bridge sends them to (/chat-link#<code>): signed in, they see which
// account the code is for and confirm it. Their linked accounts are listed on their account page, where they can remove one.

const chatLinkGone = "This link is wrong, expired or was used already. Ask for a new one in the chat."

type chatAccountJSON struct {
	Bridge  string    `json:"bridge"`
	Account string    `json:"account"`
	Name    string    `json:"name"`
	Linked  time.Time `json:"linked"`
}

// peekChatLink says which chat account a code is for.
func (s *Server) peekChatLink(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Code string }
	if !decode(w, r, &body) {
		return
	}
	c, err := s.hub.BridgeCodeFor(r.Context(), body.Code)
	if errors.Is(err, hub.ErrBadBridgeCode) {
		s.apiFail(w, http.StatusNotFound, "bad_code", chatLinkGone)
		return
	} else if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bridge": c.Bridge, "name": c.ExternalName, "expires": c.Expires.UTC()})
}

// confirmChatLink links the code's chat account to whoever is signed in.
func (s *Server) confirmChatLink(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Code string }
	if !decode(w, r, &body) {
		return
	}
	a, err := s.hub.ConfirmBridgeCode(r.Context(), user, body.Code)
	if errors.Is(err, hub.ErrBadBridgeCode) {
		s.apiFail(w, http.StatusNotFound, "bad_code", chatLinkGone)
		return
	} else if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, chatAccountJSON{Bridge: a.Bridge, Account: a.ExternalID, Name: a.ExternalName, Linked: a.Linked.UTC()})
}

// chatAccounts lists the chat accounts the user linked.
func (s *Server) chatAccounts(w http.ResponseWriter, r *http.Request, user store.User) {
	list, err := s.hub.BridgeAccountsOf(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := make([]chatAccountJSON, 0, len(list))
	for _, a := range list {
		out = append(out, chatAccountJSON{Bridge: a.Bridge, Account: a.ExternalID, Name: a.ExternalName, Linked: a.Linked.UTC()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

// unlinkChatAccount removes one of the user's chat accounts.
func (s *Server) unlinkChatAccount(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.UnlinkBridgeAccount(r.Context(), user, r.PathValue("bridge"), r.PathValue("account")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
