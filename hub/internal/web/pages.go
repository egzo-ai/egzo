package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// projectURL is the page of a project: the engine is part of the address.
func projectURL(engine, project string) string {
	return "/e/" + url.PathEscape(engine) + "/p/" + url.PathEscape(project)
}

func agentURL(engine, project, agent string) string {
	return projectURL(engine, project) + "/a/" + url.PathEscape(agent)
}

// who names the actor of an agent or an audit entry for people.
type namer map[string]string

func (s *Server) namer(ctx context.Context) namer {
	n := namer{}
	users, _ := s.hub.Store().Users(ctx)
	for _, u := range users {
		n[u.ID] = u.DisplayName()
	}
	return n
}

func (n namer) actor(actor string) string {
	switch {
	case actor == "" || actor == rbac.Operator:
		return "operator (CLI)"
	case actor == "hub":
		return "the hub"
	}
	if id, ok := rbac.UserOf(actor); ok {
		if name, ok := n[id]; ok {
			return name
		}
		return "a removed user"
	}
	return actor
}

// --- home --------------------------------------------------------------------------------------------------------------------

func (s *Server) home(w http.ResponseWriter, r *http.Request, user store.User) {
	refs, down, err := s.hub.Projects(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	filter := r.URL.Query().Get("engine")
	if filter != "" {
		kept := refs[:0:0]
		for _, ref := range refs {
			if ref.Engine == filter {
				kept = append(kept, ref)
			}
		}
		refs = kept
	}
	type row struct {
		Engine, Name, Role, URL string
		Agents, Running         int
		Up                      bool
	}
	rows := make([]row, 0, len(refs))
	for _, ref := range refs {
		r := row{Engine: ref.Engine, Name: ref.Project.Name, Role: string(ref.Role), URL: projectURL(ref.Engine, ref.Project.Name), Agents: len(ref.Project.Agents), Up: ref.Project.Up}
		for _, a := range ref.Project.Agents {
			if a.Running() {
				r.Running++
			}
		}
		rows = append(rows, r)
	}
	s.render(w, r, http.StatusOK, "home", map[string]any{"Title": "Projects", "Rows": rows, "Down": down, "Filter": filter})
}

// --- project -----------------------------------------------------------------------------------------------------------------

type eventRow struct {
	Time   string
	Type   string
	Agent  string
	Actor  string
	Text   string
	AgentL string
}

func (s *Server) eventRows(events []engines.Event, n namer) []eventRow {
	rows := make([]eventRow, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- { // newest first
		e := events[i]
		rows = append(rows, eventRow{Time: e.Time.Local().Format("15:04:05"), Type: e.Type, Agent: e.Agent, Actor: n.actor(e.Actor), Text: e.Text})
	}
	return rows
}

type agentRow struct {
	hub.AgentView
	URL      string
	Creator  string
	Waiting  bool
	Activity string
}

func (s *Server) projectPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, name := r.PathValue("engine"), r.PathValue("project")
	view, err := s.hub.Project(r.Context(), user, engine, name)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	rows := make([]agentRow, 0, len(view.Agents))
	for _, a := range view.Agents {
		rows = append(rows, agentRow{AgentView: a, URL: agentURL(engine, name, a.Name), Creator: n.actor(a.Actor), Waiting: a.Waiting, Activity: a.Activity})
	}
	events, _ := s.hub.Events(r.Context(), user, engine, name, "", 30)
	s.render(w, r, http.StatusOK, "project", map[string]any{
		"Title": name, "View": view, "Rows": rows, "Events": s.eventRows(events, n),
		"Base": projectURL(engine, name), "Admin": view.Role.AtLeast(rbac.Administrator),
	})
}

func (s *Server) spawn(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	r.ParseForm()
	template, name := strings.TrimSpace(r.Form.Get("template")), strings.TrimSpace(r.Form.Get("name"))
	spawned, err := s.hub.Spawn(r.Context(), user, engine, project, template, name)
	if errors.Is(err, hub.ErrNotFound) || errors.Is(err, hub.ErrForbidden) {
		s.problem(w, r, err)
		return
	}
	if err != nil {
		back(w, r, projectURL(engine, project), "err", "Could not spawn: "+err.Error())
		return
	}
	back(w, r, agentURL(engine, project, spawned), "ok", "Spawned "+spawned)
}

func (s *Server) operate(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, agent, verb := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent"), r.PathValue("verb")
	if verb != "start" && verb != "stop" && verb != "rm" {
		s.fail(w, r, http.StatusNotFound, "There is nothing here.")
		return
	}
	err := s.hub.Operate(r.Context(), user, verb, engine, project, agent)
	if errors.Is(err, hub.ErrNotFound) || errors.Is(err, hub.ErrForbidden) {
		s.problem(w, r, err)
		return
	}
	to := agentURL(engine, project, agent)
	if verb == "rm" && err == nil {
		to = projectURL(engine, project)
	}
	if err != nil {
		back(w, r, to, "err", "Could not "+verb+" "+agent+": "+err.Error())
		return
	}
	done := map[string]string{"start": "Started ", "stop": "Stopped ", "rm": "Removed "}[verb]
	back(w, r, to, "ok", done+agent)
}

// --- agent -------------------------------------------------------------------------------------------------------------------

func (s *Server) agentPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, name := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent")
	view, agent, err := s.hub.Agent(r.Context(), user, engine, project, name)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	events, _ := s.hub.Events(r.Context(), user, engine, project, name, 40)
	viewers, _ := s.hub.Viewers(r.Context(), user, engine, project, name)
	s.render(w, r, http.StatusOK, "agent", map[string]any{
		"Title": name, "View": view, "Agent": agent, "Creator": n.actor(agent.Actor), "Events": s.eventRows(events, n),
		"Viewers": viewers, "Base": projectURL(engine, project), "Self": agentURL(engine, project, name),
		"Terminal": agent.Running(),
	})
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, name := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent")
	out, err := s.hub.Logs(r.Context(), user, engine, project, name, 400)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "text", map[string]any{"Title": "Logs of " + name, "Text": out, "Back": agentURL(engine, project, name)})
}

func (s *Server) specPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	spec, err := s.hub.Spec(r.Context(), user, engine, project)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "text", map[string]any{"Title": "Spec of " + project, "Text": string(spec), "Back": projectURL(engine, project)})
}

// --- members and audit -------------------------------------------------------------------------------------------------------

func (s *Server) membersPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	members, err := s.hub.Members(r.Context(), user, engine, project)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	candidates, _ := s.hub.Candidates(r.Context(), user, engine, project)
	s.render(w, r, http.StatusOK, "members", map[string]any{
		"Title": "Members of " + project, "Members": members, "Candidates": candidates, "Roles": rbac.Roles,
		"Base": projectURL(engine, project), "Me": user.ID,
	})
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	r.ParseForm()
	target, role := r.Form.Get("user"), rbac.Role(r.Form.Get("role"))
	if role == "none" {
		role = rbac.None
	}
	err := s.hub.SetRole(r.Context(), user, engine, project, target, role)
	if errors.Is(err, hub.ErrForbidden) && target == user.ID {
		back(w, r, projectURL(engine, project)+"/members", "err", "An owner cannot remove or lower themselves.")
		return
	}
	if err != nil {
		s.problem(w, r, err)
		return
	}
	back(w, r, projectURL(engine, project)+"/members", "ok", "Roles updated")
}

func (s *Server) auditPage(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	entries, err := s.hub.Audit(r.Context(), user, engine, project, 100)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	type row struct {
		At, Actor, Action, Agent, Detail string
		Allowed                          bool
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, row{At: e.At.Local().Format("2006-01-02 15:04:05"), Actor: n.actor(e.Actor), Action: e.Action, Agent: e.Agent, Detail: e.Detail, Allowed: e.Allowed})
	}
	s.render(w, r, http.StatusOK, "audit", map[string]any{"Title": "Audit of " + project, "Rows": rows, "Base": projectURL(engine, project)})
}

// --- engines -----------------------------------------------------------------------------------------------------------------

func (s *Server) enginesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	if !user.IsOperator {
		s.problem(w, r, hub.ErrForbidden)
		return
	}
	s.renderEngines(w, r, http.StatusOK, hub.EngineInput{}, nil, "")
}

func (s *Server) renderEngines(w http.ResponseWriter, r *http.Request, status int, form hub.EngineInput, pending *pendingEngine, token string) {
	engs, _ := s.hub.Engines(r.Context())
	form.CA, form.Cert, form.Key, form.SSHKey = "", "", "", "" // credentials are never shown again
	s.render(w, r, status, "engines", map[string]any{"Title": "Engines", "List": engs, "Form": form, "Pending": pending, "Token": token})
}

// pendingEngine is an ssh engine waiting for its host key to be confirmed. What the operator typed stays here, in memory, for a
// few minutes: the private key never goes back to the browser.
type pendingEngine struct {
	Input       hub.EngineInput
	Fingerprint string
	expires     time.Time
}

func (s *Server) keepPending(in hub.EngineInput, fingerprint string) string {
	token := auth.Random()
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	now := time.Now()
	for k, p := range s.pending {
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
	s.pending[token] = &pendingEngine{Input: in, Fingerprint: fingerprint, expires: now.Add(10 * time.Minute)}
	return token
}

func (s *Server) takePending(token string) *pendingEngine {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.pending[token]
	delete(s.pending, token)
	if p == nil || time.Now().After(p.expires) {
		return nil
	}
	return p
}

func (s *Server) saveEngine(w http.ResponseWriter, r *http.Request, user store.User) {
	if !user.IsOperator {
		s.problem(w, r, hub.ErrForbidden)
		return
	}
	r.ParseForm()
	in := hub.EngineInput{
		Name: r.Form.Get("name"), Host: r.Form.Get("host"), SSHUser: r.Form.Get("ssh_user"),
		CA: r.Form.Get("ca"), Cert: r.Form.Get("cert"), Key: r.Form.Get("key"), SSHKey: r.Form.Get("ssh_key"),
	}
	s.saveAndReport(w, r, user, in)
}

// confirmEngine pins the host key the operator was shown and saves the engine.
func (s *Server) confirmEngine(w http.ResponseWriter, r *http.Request, user store.User) {
	if !user.IsOperator {
		s.problem(w, r, hub.ErrForbidden)
		return
	}
	r.ParseForm()
	p := s.takePending(r.Form.Get("token"))
	if p == nil {
		back(w, r, "/engines", "err", "That confirmation expired. Add the engine again.")
		return
	}
	in := p.Input
	in.PinHostKey = p.Fingerprint
	s.saveAndReport(w, r, user, in)
}

func (s *Server) saveAndReport(w http.ResponseWriter, r *http.Request, user store.User, in hub.EngineInput) {
	err := s.hub.SaveEngine(r.Context(), user, in)
	var pending *hub.HostKeyPending
	switch {
	case errors.As(err, &pending):
		token := s.keepPending(in, pending.Fingerprint)
		s.renderEngines(w, r, http.StatusOK, in, &pendingEngine{Input: hub.EngineInput{Name: in.Name, Host: in.Host}, Fingerprint: pending.Fingerprint}, token)
	case errors.Is(err, hub.ErrForbidden):
		s.problem(w, r, err)
	case err != nil:
		r.URL.RawQuery = url.Values{"err": {err.Error()}}.Encode()
		s.renderEngines(w, r, http.StatusUnprocessableEntity, in, nil, "")
	default:
		back(w, r, "/engines", "ok", "Saved engine "+in.Name)
	}
}

func (s *Server) removeEngine(w http.ResponseWriter, r *http.Request, user store.User) {
	err := s.hub.RemoveEngine(r.Context(), user, r.PathValue("name"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	back(w, r, "/engines", "ok", "Removed engine "+r.PathValue("name"))
}
