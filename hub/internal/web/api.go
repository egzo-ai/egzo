package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// The JSON API under /api/v1. Every answer is JSON; every error is {"code", "message"} with a real status code. Times are RFC 3339.
// The shapes below are the contract with the front end (hub/web/src/lib/types.ts); docs/api.md describes them.

const apiPrefix = "/api/v1"

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) apiFail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}

// problem turns an error of the hub into an answer.
func (s *Server) problem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, hub.ErrNotFound):
		s.apiFail(w, http.StatusNotFound, "not_found", "There is nothing here, or you have no access to it.")
	case errors.Is(err, hub.ErrForbidden):
		s.apiFail(w, http.StatusForbidden, "forbidden", "You are not allowed to do that.")
	case errors.Is(err, hub.ErrOperatorAccess):
		s.apiFail(w, http.StatusForbidden, "forbidden", "An operator holds every project. Change that under Settings, Users, not as a role.")
	case errors.Is(err, hub.ErrLastOperator):
		s.apiFail(w, http.StatusConflict, "conflict", "The last operator cannot be removed. Make somebody else an operator first.")
	default:
		s.log.Error("request", "path", r.URL.Path, "err", err)
		s.apiFail(w, http.StatusBadGateway, "engine_error", err.Error())
	}
}

// decode reads a JSON body of a state-changing request.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", Message: "The request body is not valid JSON."})
		return false
	}
	return true
}

// jsonOnly refuses a state-changing API request that is not JSON: a cross-site form cannot send that type without a preflight,
// which is a second wall behind the Origin check.
func (s *Server) jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix+"/") && changesState(r.Method) {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				s.apiFail(w, http.StatusUnsupportedMediaType, "bad_request", "Send application/json.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func changesState(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

// --- shapes ------------------------------------------------------------------------------------------------------------------

type userJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	IsOperator bool   `json:"isOperator"`
}

func userOf(u store.User) userJSON {
	return userJSON{ID: u.ID, Name: u.DisplayName(), Email: u.Email, IsOperator: u.IsOperator}
}

type engineJSON struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"` // operators only
	Up   bool   `json:"up"`
	Err  string `json:"err,omitempty"`
}

type agentJSON struct {
	Name       string    `json:"name"`
	Template   string    `json:"template"`
	State      string    `json:"state"`
	Running    bool      `json:"running"`
	Activity   string    `json:"activity"`
	Status     string    `json:"status"`
	Waiting    bool      `json:"waiting"`
	Stale      bool      `json:"stale"`
	Creator    string    `json:"creator"`
	CreatedAt  time.Time `json:"createdAt"`
	CanOperate bool      `json:"canOperate"`
	Holder     string    `json:"holder"`
}

type serviceJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

type projectJSON struct {
	Engine    string        `json:"engine"`
	Name      string        `json:"name"`
	Role      rbac.Role     `json:"role"`
	Operator  bool          `json:"operator"` // held as an operator, without a grant
	Up        bool          `json:"up"`
	Templates []string      `json:"templates"`
	Services  []serviceJSON `json:"services"`
	Agents    []agentJSON   `json:"agents"`
	CanSpawn  bool          `json:"canSpawn"`
	CanManage bool          `json:"canManage"`
	CanAudit  bool          `json:"canAudit"`
}

func (s *Server) agentOf(a hub.AgentView, n namer) agentJSON {
	return agentJSON{Name: a.Name, Template: a.Template, State: a.State, Running: a.Running(), Activity: a.Activity, Status: a.Status,
		Waiting: a.Waiting, Stale: a.Stale, Creator: n.actor(a.Actor), CreatedAt: a.Created, CanOperate: a.CanOperate, Holder: a.Holder}
}

func (s *Server) projectOf(v hub.ProjectView, n namer) projectJSON {
	p := projectJSON{Engine: v.Engine, Name: v.Project.Name, Role: v.Role, Operator: v.Operator, Up: v.Project.Up, Templates: v.Project.Templates,
		Services: []serviceJSON{}, Agents: []agentJSON{}, CanSpawn: v.CanSpawn, CanManage: v.CanManage, CanAudit: v.Role.AtLeast(rbac.Administrator)}
	if p.Templates == nil {
		p.Templates = []string{}
	}
	for _, svc := range v.Project.Services {
		p.Services = append(p.Services, serviceJSON{Name: svc.Name, State: svc.State, Health: svc.Health})
	}
	for _, a := range v.Agents {
		p.Agents = append(p.Agents, s.agentOf(a, n))
	}
	return p
}

// --- session -----------------------------------------------------------------------------------------------------------------

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, user store.User) {
	views, _ := s.hub.Engines(r.Context())
	list := make([]engineJSON, 0, len(views))
	for _, v := range views {
		list = append(list, engineJSON{Name: v.Name, Up: v.Up, Err: v.Err})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userOf(user), "engines": list})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.hub.Store().EndSession(r.Context(), c.Value)
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, "", -time.Hour))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- projects ----------------------------------------------------------------------------------------------------------------

// projects lists what the user may see from the last scan, with each agent's activity: it feeds the home page and the sidebar and
// calls no engine.
func (s *Server) projects(w http.ResponseWriter, r *http.Request, user store.User) {
	refs, down, err := s.hub.Projects(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type agent struct {
		Name     string `json:"name"`
		Template string `json:"template"`
		Running  bool   `json:"running"`
		Activity string `json:"activity"`
		Status   string `json:"status"`
		Waiting  bool   `json:"waiting"`
	}
	type project struct {
		Engine   string    `json:"engine"`
		Name     string    `json:"name"`
		Role     rbac.Role `json:"role"`
		Operator bool      `json:"operator"`
		Up       bool      `json:"up"`
		Agents   []agent   `json:"agents"`
	}
	out := struct {
		Projects    []project    `json:"projects"`
		Unreachable []engineJSON `json:"unreachable"`
	}{Projects: []project{}, Unreachable: []engineJSON{}}
	for _, ref := range refs {
		p := project{Engine: ref.Engine, Name: ref.Project.Name, Role: ref.Role, Operator: ref.Operator, Up: ref.Project.Up, Agents: []agent{}}
		for _, a := range ref.Project.Agents {
			p.Agents = append(p.Agents, agent{Name: a.Name, Template: a.Template, Running: a.Running(), Activity: a.Activity, Status: a.Status, Waiting: a.Waiting})
		}
		out.Projects = append(out.Projects, p)
	}
	for _, e := range down {
		out.Unreachable = append(out.Unreachable, engineJSON{Name: e.Name, Up: false, Err: e.Err})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, user store.User) {
	view, err := s.hub.Project(r.Context(), user, r.PathValue("engine"), r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectOf(view, s.namer(r.Context())))
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, name := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent")
	view, agent, err := s.hub.Agent(r.Context(), user, engine, project, name)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	p := s.projectOf(view, n)
	p.Agents = nil // the project carries the other agents; this answer is about one
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "agent": s.agentOf(agent, n)})
}

type eventJSON struct {
	Time  time.Time `json:"time"`
	Type  string    `json:"type"`
	Agent string    `json:"agent"`
	Actor string    `json:"actor"`
	Text  string    `json:"text"`
}

// events answers the timeline of a project, or of one agent with ?agent=, newest first.
func (s *Server) events(w http.ResponseWriter, r *http.Request, user store.User) {
	limit := 40
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	events, err := s.hub.Events(r.Context(), user, r.PathValue("engine"), r.PathValue("project"), r.URL.Query().Get("agent"), limit)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": s.eventRows(events, s.namer(r.Context()))})
}

func (s *Server) eventRows(events []engines.Event, n namer) []eventJSON {
	rows := make([]eventJSON, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- { // newest first
		e := events[i]
		rows = append(rows, eventJSON{Time: e.Time, Type: e.Type, Agent: e.Agent, Actor: n.actor(e.Actor), Text: e.Text})
	}
	return rows
}

func (s *Server) spawn(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Template, Name string }
	if !decode(w, r, &body) {
		return
	}
	spawned, err := s.hub.Spawn(r.Context(), user, r.PathValue("engine"), r.PathValue("project"), strings.TrimSpace(body.Template), strings.TrimSpace(body.Name))
	if errors.Is(err, hub.ErrNotFound) || errors.Is(err, hub.ErrForbidden) {
		s.problem(w, r, err)
		return
	}
	if err != nil {
		s.apiFail(w, http.StatusBadGateway, "operation_failed", "Could not spawn: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"agent": spawned})
}

func (s *Server) operate(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project, agent, verb := r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent"), r.PathValue("verb")
	if verb != "start" && verb != "stop" && verb != "rm" {
		s.apiFail(w, http.StatusNotFound, "not_found", "There is nothing here.")
		return
	}
	err := s.hub.Operate(r.Context(), user, verb, engine, project, agent)
	if errors.Is(err, hub.ErrNotFound) || errors.Is(err, hub.ErrForbidden) {
		s.problem(w, r, err)
		return
	}
	if err != nil {
		s.apiFail(w, http.StatusBadGateway, "operation_failed", "Could not "+verb+" "+agent+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"agent": agent, "verb": verb})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, user store.User) {
	out, err := s.hub.Logs(r.Context(), user, r.PathValue("engine"), r.PathValue("project"), r.PathValue("agent"), 400)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": out})
}

func (s *Server) spec(w http.ResponseWriter, r *http.Request, user store.User) {
	spec, err := s.hub.Spec(r.Context(), user, r.PathValue("engine"), r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": string(spec)})
}

// --- members and audit -------------------------------------------------------------------------------------------------------

func (s *Server) members(w http.ResponseWriter, r *http.Request, user store.User) {
	engine, project := r.PathValue("engine"), r.PathValue("project")
	members, err := s.hub.Members(r.Context(), user, engine, project)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	candidates, _ := s.hub.Candidates(r.Context(), user, engine, project)
	type member struct {
		User     userJSON  `json:"user"`
		Role     rbac.Role `json:"role"`
		Operator bool      `json:"operator"` // holds the project as an operator: not a role, cannot be changed here
	}
	out := struct {
		Members    []member    `json:"members"`
		Candidates []userJSON  `json:"candidates"`
		Roles      []rbac.Role `json:"roles"`
		Me         string      `json:"me"`
	}{Members: []member{}, Candidates: []userJSON{}, Roles: rbac.Roles, Me: user.ID}
	for _, m := range members {
		out.Members = append(out.Members, member{User: userOf(m.User), Role: m.Role, Operator: m.Operator})
	}
	for _, c := range candidates {
		out.Candidates = append(out.Candidates, userOf(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		User string
		Role string // a role, or "none" to remove
	}
	if !decode(w, r, &body) {
		return
	}
	role := rbac.Role(body.Role)
	if body.Role == "none" {
		role = rbac.None
	}
	err := s.hub.SetRole(r.Context(), user, r.PathValue("engine"), r.PathValue("project"), body.User, role)
	if errors.Is(err, hub.ErrForbidden) && body.User == user.ID {
		s.apiFail(w, http.StatusForbidden, "forbidden", "An owner cannot remove or lower themselves.")
		return
	}
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, user store.User) {
	entries, err := s.hub.Audit(r.Context(), user, r.PathValue("engine"), r.PathValue("project"), 100)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	type row struct {
		At      time.Time `json:"at"`
		Actor   string    `json:"actor"`
		Action  string    `json:"action"`
		Agent   string    `json:"agent"`
		Detail  string    `json:"detail"`
		Allowed bool      `json:"allowed"`
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, row{At: e.At, Actor: n.actor(e.Actor), Action: e.Action, Agent: e.Agent, Detail: e.Detail, Allowed: e.Allowed})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
}

// namer names the actor of an agent or an audit entry for people.
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

// --- users -------------------------------------------------------------------------------------------------------------------

type userRowJSON struct {
	userJSON
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, user store.User) {
	users, err := s.hub.Users(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	rows := make([]userRowJSON, 0, len(users))
	for _, u := range users {
		rows = append(rows, userRowJSON{userJSON: userOf(u), CreatedAt: u.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": rows, "me": user.ID})
}

func (s *Server) setOperator(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		IsOperator *bool `json:"isOperator"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.IsOperator == nil {
		s.apiFail(w, http.StatusBadRequest, "bad_request", "Say isOperator: true or false.")
		return
	}
	if err := s.hub.SetOperator(r.Context(), user, r.PathValue("id"), *body.IsOperator); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- engines -----------------------------------------------------------------------------------------------------------------

func (s *Server) operatorOnly(next authedHandler) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, user store.User) {
		if !user.IsOperator {
			s.problem(w, r, hub.ErrForbidden)
			return
		}
		next(w, r, user)
	}
}

func (s *Server) listEngines(w http.ResponseWriter, r *http.Request, _ store.User) {
	views, err := s.hub.Engines(r.Context())
	if err != nil {
		s.problem(w, r, err)
		return
	}
	list := make([]engineJSON, 0, len(views))
	for _, v := range views {
		list = append(list, engineJSON{Name: v.Name, Host: v.Host, Up: v.Up, Err: v.Err})
	}
	writeJSON(w, http.StatusOK, map[string]any{"engines": list})
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
	var body struct {
		Name, Host, SSHUser   string
		CA, Cert, Key, SSHKey string
	}
	if !decode(w, r, &body) {
		return
	}
	s.saveAndReport(w, r, user, hub.EngineInput{Name: body.Name, Host: body.Host, SSHUser: body.SSHUser, CA: body.CA, Cert: body.Cert, Key: body.Key, SSHKey: body.SSHKey})
}

// confirmEngine pins the host key the operator was shown and saves the engine.
func (s *Server) confirmEngine(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Token string }
	if !decode(w, r, &body) {
		return
	}
	p := s.takePending(body.Token)
	if p == nil {
		s.apiFail(w, http.StatusGone, "expired", "That confirmation expired. Add the engine again.")
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
		writeJSON(w, http.StatusOK, map[string]any{"pending": map[string]string{"name": in.Name, "host": in.Host, "fingerprint": pending.Fingerprint, "token": token}})
	case errors.Is(err, hub.ErrForbidden):
		s.problem(w, r, err)
	case err != nil:
		s.apiFail(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"saved": in.Name})
	}
}

func (s *Server) removeEngine(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.RemoveEngine(r.Context(), user, r.PathValue("name")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": r.PathValue("name")})
}
