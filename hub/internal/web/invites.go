package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// inviteGone is what anyone hears of a code that cannot be spent.
const inviteGone = "This invite is no longer good: it was used, it expired or it was revoked. Ask for a new one."

// invitePath is the invite page. The code goes after a # in the link: a browser sends that part to nobody, so no proxy's access log or Referer
// header holds a live code, and the page sends it in a body. Everything that takes a code here is a POST for the same reason.
const invitePath = "/invite#"

// inviteJSON is an invite as it is listed, or shown to whoever holds its code. Project and Role are set for a role on a project, Operator and
// ProjectQuota for the flags on a user.
type inviteJSON struct {
	ID           string    `json:"id"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Project      string    `json:"project,omitempty"`
	ProjectName  string    `json:"projectName,omitempty"`
	Role         rbac.Role `json:"role,omitempty"`
	Operator     bool      `json:"operator"`
	ProjectQuota int       `json:"projectQuota"`
}

func inviteOf(v hub.InviteView) inviteJSON {
	return inviteJSON{ID: v.ID, CreatedBy: v.CreatorName, CreatedAt: v.Created, ExpiresAt: v.Expires, Project: v.Project, ProjectName: v.ProjectName, Role: v.Role,
		Operator: v.Operator, ProjectQuota: v.ProjectQuota}
}

func (s *Server) writeInvites(w http.ResponseWriter, r *http.Request, views []hub.InviteView, err error) {
	if err != nil {
		s.problem(w, r, err)
		return
	}
	rows := make([]inviteJSON, 0, len(views))
	for _, v := range views {
		rows = append(rows, inviteOf(v))
	}
	writeJSON(w, http.StatusOK, map[string][]inviteJSON{"invites": rows})
}

// writeMade answers a new invite: the only time its link is seen.
func (s *Server) writeMade(w http.ResponseWriter, r *http.Request, made store.Invite, code string, err error) {
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": made.ID, "url": s.urls.Base(r) + invitePath + code, "expiresAt": made.Expires})
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request, user store.User) {
	views, err := s.hub.Invites(r.Context(), user)
	s.writeInvites(w, r, views, err)
}

func (s *Server) projectInvites(w http.ResponseWriter, r *http.Request, user store.User) {
	views, err := s.hub.ProjectInvites(r.Context(), user, r.PathValue("project"))
	s.writeInvites(w, r, views, err)
}

func (s *Server) createHubInvite(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		Operator     bool `json:"operator"`
		ProjectQuota int  `json:"projectQuota"`
	}
	if !decode(w, r, &body) {
		return
	}
	made, code, err := s.hub.InviteToHub(r.Context(), user, body.Operator, body.ProjectQuota)
	s.writeMade(w, r, made, code, err)
}

func (s *Server) createProjectInvite(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		Role rbac.Role `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	made, code, err := s.hub.InviteToProject(r.Context(), user, r.PathValue("project"), body.Role)
	s.writeMade(w, r, made, code, err)
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.RevokeInvite(r.Context(), user, r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// peekInvite says what a code grants and the ways to take it. It needs no session: the code is the proof.
func (s *Server) peekInvite(w http.ResponseWriter, r *http.Request) {
	var body struct{ Code string }
	if !decode(w, r, &body) {
		return
	}
	view, err := s.hub.PeekInvite(r.Context(), body.Code)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	sso := ""
	if s.auth != nil {
		sso = s.opts.SSOName
	}
	writeJSON(w, http.StatusOK, map[string]any{"invite": inviteOf(view), "local": s.hub.SignIn().LocalLogin, "sso": sso})
}

// acceptInvite spends a code on whoever is signed in.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Code string }
	if !decode(w, r, &body) {
		return
	}
	view, err := s.hub.AcceptInvite(r.Context(), user, body.Code)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]inviteJSON{"invite": inviteOf(view)})
}

// registerInvite makes an account with a password from a code, and signs its owner in.
func (s *Server) registerInvite(w http.ResponseWriter, r *http.Request) {
	var body struct{ Code, Username, Password string }
	if !decode(w, r, &body) {
		return
	}
	user, err := s.hub.RegisterLocal(r.Context(), body.Code, strings.TrimSpace(body.Username), body.Password)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	token, err := s.hub.Store().NewSession(r.Context(), user.ID, store.MethodPassword, "", s.session)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, token, s.session))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// inviteSSO begins a sign-in at the provider with a code, which goes in the login cookie: the app sends the browser to the address it answers.
func (s *Server) inviteSSO(w http.ResponseWriter, r *http.Request) {
	var body struct{ Code string }
	if !decode(w, r, &body) {
		return
	}
	if s.auth == nil {
		s.apiFail(w, http.StatusNotFound, "not_found", "Single sign-on is not configured.")
		return
	}
	if _, err := s.hub.PeekInvite(r.Context(), body.Code); err != nil {
		s.problem(w, r, err)
		return
	}
	target, err := s.providerURL(w, r, "/", "", body.Code)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": target})
}
