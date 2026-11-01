package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/links"
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
	case errors.Is(err, hub.ErrLastOwner):
		s.apiFail(w, http.StatusConflict, "conflict", "A project keeps at least one owner. Make somebody else an owner first.")
	case errors.Is(err, hub.ErrOverQuota):
		s.apiFail(w, http.StatusConflict, "over_quota", "You have as many projects as your quota allows. An operator can raise it.")
	case errors.Is(err, hub.ErrBadQuota):
		s.apiFail(w, http.StatusBadRequest, "bad_request", "A quota is zero or more.")
	case errors.Is(err, hub.ErrBadRole):
		s.apiFail(w, http.StatusBadRequest, "bad_request", "That is not a role.")
	case errors.Is(err, hub.ErrBadInvite):
		s.apiFail(w, http.StatusNotFound, "bad_invite", inviteGone)
	case errors.Is(err, hub.ErrUsernameTaken):
		s.apiFail(w, http.StatusConflict, "conflict", "That username is taken. Pick another.")
	case errors.Is(err, hub.ErrBadUsername), errors.Is(err, hub.ErrWeakPassword):
		s.apiFail(w, http.StatusBadRequest, "bad_request", capitalize(err.Error())+".")
	case errors.Is(err, hub.ErrNoLocalLogin):
		s.apiFail(w, http.StatusNotFound, "not_found", "Signing in with a password is turned off.")
	case errors.Is(err, hub.ErrLastOperator):
		s.apiFail(w, http.StatusConflict, "conflict", "The last operator cannot be removed. Make somebody else an operator first.")
	case errors.Is(err, hub.ErrCannotDisable):
		s.apiFail(w, http.StatusConflict, "conflict", "An operator cannot be disabled. Remove operator first.")
	case errors.Is(err, hub.ErrDisabled):
		s.apiFail(w, http.StatusConflict, "conflict", "That user is disabled. Enable them first.")
	case errors.Is(err, hub.ErrLastWayIn):
		s.apiFail(w, http.StatusConflict, "conflict", "That is your last way to sign in: link another account first.")
	case errors.Is(err, hub.ErrTaskRunning):
		s.apiFail(w, http.StatusConflict, "conflict", "The task is still running: it ends on its own.")
	case errors.Is(err, hub.ErrNameRequired):
		s.apiFail(w, http.StatusBadRequest, "bad_request", "Give the agent a name.")
	case errors.Is(err, hub.ErrBadName):
		s.apiFail(w, http.StatusBadRequest, "bad_request", "That name is not valid: "+err.Error()+".")
	case errors.As(err, new(hub.ExistsError)):
		s.apiFail(w, http.StatusConflict, "conflict", "There is already an agent named "+errorAs[hub.ExistsError](err).Agent+". Pick another name.")
	case errors.As(err, new(hub.BusyError)):
		t := errorAs[hub.BusyError](err).Task
		s.apiFail(w, http.StatusConflict, "busy", fmt.Sprintf("%s is being %s by %s. Wait for it to end.", t.Agent, beingDone[t.Verb], s.namer(r.Context()).actor(t.Actor)))
	case errors.Is(err, hub.ErrNotDisabled):
		s.apiFail(w, http.StatusConflict, "conflict", "Only a disabled user's account can be removed. Disable the user first.")
	case errors.Is(err, hub.ErrNotTurnedAway):
		s.apiFail(w, http.StatusConflict, "conflict", "Only a project that was refused or revoked can be forgotten. Revoke it first.")
	case errors.As(err, new(hub.OfflineError)):
		s.apiFail(w, http.StatusConflict, "offline", "The project is offline: its link is not connected. Run egzo project up on its machine.")
	default:
		s.log.Error("request", "path", r.URL.Path, "err", err)
		s.apiFail(w, http.StatusBadGateway, "link_error", err.Error())
	}
}

func errorAs[E error](err error) E {
	var e E
	errors.As(err, &e)
	return e
}

// beingDone says what a task does to its agent: "agent-3 is being removed".
var beingDone = map[string]string{hub.VerbSpawn: "made", hub.VerbStart: "started", hub.VerbStop: "stopped", hub.VerbRm: "removed"}

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

// userJSON is a person: the username the hub chose, then what their provider says (from the identity they signed in with most
// recently, display only).
type userJSON struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	Picture       string `json:"picture"`
	IsOperator    bool   `json:"isOperator"`
	Disabled      bool   `json:"disabled"`
	ProjectQuota  int    `json:"projectQuota"`
}

func userOf(u store.User) userJSON {
	return userJSON{ID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email, EmailVerified: u.EmailVerified, Picture: safePicture(u.Picture),
		IsOperator: u.IsOperator, Disabled: u.Disabled(), ProjectQuota: u.ProjectQuota}
}

// safePicture keeps an avatar address only when it is https: the browser fetches it, never the hub (which could be made to
// reach what only it can reach).
func safePicture(p string) string {
	if u, err := url.Parse(p); err == nil && u.Scheme == "https" && u.Host != "" {
		return p
	}
	return ""
}

// identityJSON is one account at a provider. Current is false for an identity of another issuer than the configured one: it
// cannot sign in.
type identityJSON struct {
	ID          string    `json:"id"`
	Issuer      string    `json:"issuer"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Current     bool      `json:"current"`
	LastLoginAt time.Time `json:"lastLoginAt"`
}

func (s *Server) identitiesOf(list []store.Identity) []identityJSON {
	issuer := s.hub.SignIn().Issuer
	out := []identityJSON{}
	for _, i := range list {
		out = append(out, identityJSON{ID: i.ID, Issuer: i.Issuer, Email: i.Email, Name: i.Name, Current: issuer != "" && i.Issuer == issuer, LastLoginAt: i.LastLoginAt})
	}
	return out
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
	Denied     int       `json:"denied"` // hosts the proxy refused it in the last hour, not reached since
	// Task is what the hub does on the agent, or the failure of what it did that nobody dismissed. An agent with a task and no state
	// is one the project does not have: a spawn that runs or failed, or a removal that is ending.
	Task *taskJSON `json:"task,omitempty"`
}

// feedJSON says how far the agents' activity can be trusted: live, connecting, stale, unavailable or offline, and why.
type feedJSON struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

func feedOf(f hub.Feed) feedJSON { return feedJSON{State: string(f.State), Message: f.Message} }

type serviceJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

type projectJSON struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Host       string        `json:"host"`
	Role       rbac.Role     `json:"role"` // overseer: an operator with no role on it
	Ownerless  bool          `json:"ownerless,omitempty"`
	Offline    *time.Time    `json:"offlineSince"`
	Feed       feedJSON      `json:"feed"`
	Up         bool          `json:"up"`
	Templates  []string      `json:"templates"`
	Services   []serviceJSON `json:"services"`
	Agents     []agentJSON   `json:"agents"`
	CanSpawn   bool          `json:"canSpawn"`
	CanManage  bool          `json:"canManage"`
	CanUnlink  bool          `json:"canUnlink"`
	CanMembers bool          `json:"canMembers"`
	CanAudit   bool          `json:"canAudit"`
	// Ignored is what claims the project with labels egzo never writes, for whoever may audit it.
	Ignored []string `json:"ignored,omitempty"`
}

// taskJSON is one action of the hub on a project: state running, done, failed or interrupted (the hub stopped, or the link dropped, while
// it ran).
type taskJSON struct {
	ID        string     `json:"id"`
	Verb      string     `json:"verb"` // spawn, start, stop or rm
	Agent     string     `json:"agent"`
	Template  string     `json:"template,omitempty"`
	Creator   string     `json:"creator"` // who asked
	State     string     `json:"state"`
	Step      string     `json:"step,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// CanDismiss: the user may take an ended one off its agent (who may operate the agent).
	CanDismiss bool `json:"canDismiss"`
}

func taskOf(t hub.Task, n namer) taskJSON {
	j := taskJSON{ID: t.ID, Verb: t.Verb, Agent: t.Agent, Template: t.Template, Creator: n.actor(t.Actor), State: t.State, Step: t.Step, Error: t.Err,
		StartedAt: t.Started}
	if !t.Ended.IsZero() {
		j.EndedAt = &t.Ended
	}
	return j
}

func (s *Server) agentOf(a hub.AgentView, n namer) agentJSON {
	return agentJSON{Name: a.Name, Template: a.Template, State: a.State, Running: a.Running(), Activity: a.Activity, Status: a.Status,
		Waiting: a.Waiting, Stale: a.Stale, Creator: n.actor(a.Actor), CreatedAt: a.Created, CanOperate: a.CanOperate, Holder: a.Holder,
		Denied: a.Denied, Task: taskViewOf(a.Task, n)}
}

func taskViewOf(t *hub.TaskView, n namer) *taskJSON {
	if t == nil {
		return nil
	}
	j := taskOf(t.Task, n)
	j.CanDismiss = t.CanDismiss
	return &j
}

// offlineOf is when a project went offline, for an answer: null while it is connected.
func offlineOf(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

func (s *Server) projectOf(v hub.ProjectView, n namer) projectJSON {
	p := projectJSON{ID: v.ID, Name: v.Project.Name, Host: v.Host, Role: v.Role, Ownerless: v.Ownerless, Offline: offlineOf(v.Offline), Feed: feedOf(v.Feed), Up: v.Project.Up, Templates: v.Project.Templates,
		Services: []serviceJSON{}, Agents: []agentJSON{}, CanSpawn: v.CanSpawn, CanManage: v.CanManage, CanUnlink: v.CanUnlink, CanMembers: v.CanMembers, CanAudit: v.Role.AtLeast(rbac.Admin)}
	if p.Templates == nil {
		p.Templates = []string{}
	}
	for _, svc := range v.Project.Services {
		p.Services = append(p.Services, serviceJSON{Name: svc.Name, State: svc.State, Health: svc.Health})
	}
	for _, a := range v.Agents {
		p.Agents = append(p.Agents, s.agentOf(a, n))
	}
	if p.CanAudit {
		p.Ignored = v.Project.Ignored
	}
	return p
}

// --- session -----------------------------------------------------------------------------------------------------------------

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, user store.User) {
	identities, err := s.hub.Store().Identities(r.Context(), user.ID)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	connected, err := s.hub.Store().ConnectedProjects(r.Context(), user.ID)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	signIn := s.hub.SignIn()
	sso := ""
	if s.auth != nil {
		sso = s.opts.SSOName
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userOf(user), "identities": s.identitiesOf(identities),
		"hasPassword": user.HasPassword && signIn.LocalLogin, "sso": sso, "issuer": signIn.Issuer, "hub": s.opts.LinkURL,
		"canLink": user.IsOperator || connected < user.ProjectQuota, "connectedProjects": connected})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := s.readCookie(r, sessionCookie); err == nil {
		_ = s.hub.Store().EndSession(r.Context(), c.Value)
		s.endTerminals([]string{store.HashToken(c.Value)})
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, "", -time.Hour))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// unlink removes one of the signed-in user's own identities.
func (s *Server) unlink(w http.ResponseWriter, r *http.Request, user store.User) {
	ended, err := s.hub.UnlinkIdentity(r.Context(), user, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.endTerminals(ended)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- projects ----------------------------------------------------------------------------------------------------------------

// projects lists what the user may see, with each agent's activity: it feeds the home page and the sidebar and asks no link anything.
func (s *Server) projects(w http.ResponseWriter, r *http.Request, user store.User) {
	refs, err := s.hub.Projects(r.Context(), user)
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
		Task     string `json:"task,omitempty"` // the verb of the task that runs on it
	}
	type project struct {
		ID        string     `json:"id"`
		Name      string     `json:"name"`
		Host      string     `json:"host"`
		Role      rbac.Role  `json:"role"`
		Ownerless bool       `json:"ownerless,omitempty"`
		Offline   *time.Time `json:"offlineSince"`
		Feed      feedJSON   `json:"feed"`
		Up        bool       `json:"up"`
		Agents    []agent    `json:"agents"`
	}
	out := struct {
		Projects []project `json:"projects"`
	}{Projects: []project{}}
	for _, ref := range refs {
		p := project{ID: ref.ID, Name: ref.Project.Name, Host: ref.Host, Role: ref.Role, Ownerless: ref.Ownerless, Offline: offlineOf(ref.Offline), Feed: feedOf(ref.Feed), Up: ref.Project.Up, Agents: []agent{}}
		running := map[string]string{}
		for _, t := range ref.Tasks {
			running[t.Agent] = t.Verb
		}
		for _, a := range ref.Project.Agents {
			p.Agents = append(p.Agents, agent{Name: a.Name, Template: a.Template, Running: a.Running(), Activity: a.Activity, Status: a.Status, Waiting: a.Waiting,
				Task: running[a.Name]})
		}
		out.Projects = append(out.Projects, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, user store.User) {
	view, err := s.hub.Project(r.Context(), user, r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectOf(view, s.namer(r.Context())))
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request, user store.User) {
	view, agent, err := s.hub.Agent(r.Context(), user, r.PathValue("project"), r.PathValue("agent"))
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
	events, err := s.hub.Events(r.Context(), user, r.PathValue("project"), r.URL.Query().Get("agent"), limit)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": s.eventRows(events, s.namer(r.Context()))})
}

func (s *Server) eventRows(events []links.Event, n namer) []eventJSON {
	rows := make([]eventJSON, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- { // newest first
		e := events[i]
		rows = append(rows, eventJSON{Time: e.Time, Type: e.Type, Agent: e.Agent, Actor: n.actor(e.Actor), Text: e.Text})
	}
	return rows
}

// spawn is answered at once: the task runs on in the hub, and the agent is in the project from now on (hub/internal/hub/tasks.go).
func (s *Server) spawn(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct{ Template, Name string }
	if !decode(w, r, &body) {
		return
	}
	t, err := s.hub.StartSpawn(r.Context(), user, r.PathValue("project"), strings.TrimSpace(body.Template), strings.TrimSpace(body.Name))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]taskJSON{"task": taskOf(t, s.namer(r.Context()))})
}

// operate starts, stops or removes an agent: answered at once, like spawn.
func (s *Server) operate(w http.ResponseWriter, r *http.Request, user store.User) {
	project, agent, verb := r.PathValue("project"), r.PathValue("agent"), r.PathValue("verb")
	if verb != hub.VerbStart && verb != hub.VerbStop && verb != hub.VerbRm {
		s.apiFail(w, http.StatusNotFound, "not_found", "There is nothing here.")
		return
	}
	t, err := s.hub.StartOperation(r.Context(), user, verb, project, agent)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]taskJSON{"task": taskOf(t, s.namer(r.Context()))})
}

// tasks is the project's activity, or one agent's: the latest tasks, newest first.
func (s *Server) tasks(w http.ResponseWriter, r *http.Request, user store.User) {
	list, err := s.hub.Tasks(r.Context(), user, r.PathValue("project"), r.URL.Query().Get("agent"), 100)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	rows := make([]taskJSON, 0, len(list))
	for _, t := range list {
		rows = append(rows, taskOf(t, n))
	}
	writeJSON(w, http.StatusOK, map[string][]taskJSON{"tasks": rows})
}

// task is one task, which whoever asked for it follows until it ends.
func (s *Server) task(w http.ResponseWriter, r *http.Request, user store.User) {
	t, err := s.hub.TaskOf(r.Context(), user, r.PathValue("project"), r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]taskJSON{"task": taskOf(t, s.namer(r.Context()))})
}

// dismissTask takes an ended task off its agent.
func (s *Server) dismissTask(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.DismissTask(r.Context(), user, r.PathValue("project"), r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- network ---------------------------------------------------------------------------------------------------------------------

type reasonJSON struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// networkEventJSON is one event of a proxy's audit trail (roadmap/proxy-audit.md), in the web's spelling.
type networkEventJSON struct {
	Time        time.Time   `json:"time"`
	Conn        string      `json:"conn"`
	Protocol    string      `json:"protocol"`
	Event       string      `json:"event"`
	Decision    string      `json:"decision,omitempty"`
	Mode        string      `json:"mode,omitempty"`
	Agent       string      `json:"agent,omitempty"`
	Instance    string      `json:"instance,omitempty"`
	Profile     string      `json:"profile,omitempty"`
	Claimed     string      `json:"claimedAgent,omitempty"`
	Client      string      `json:"client,omitempty"`
	UserAgent   string      `json:"userAgent,omitempty"`
	Host        string      `json:"host,omitempty"`
	Service     string      `json:"service,omitempty"`
	Server      string      `json:"server,omitempty"`
	Method      string      `json:"method,omitempty"`
	Path        string      `json:"path,omitempty"`
	RawPath     string      `json:"rawPath,omitempty"`
	Status      int         `json:"status,omitempty"`
	Injected    string      `json:"injected,omitempty"`
	Substituted []string    `json:"substituted,omitempty"`
	Command     string      `json:"command,omitempty"`
	Reason      *reasonJSON `json:"reason,omitempty"`
	BytesUp     int64       `json:"bytesUp,omitempty"`
	BytesDown   int64       `json:"bytesDown,omitempty"`
	DurationMs  int64       `json:"durationMs,omitempty"`
}

type denialJSON struct {
	Instance  string     `json:"instance"`
	Agent     string     `json:"agent"`
	Claimed   string     `json:"claimedAgent,omitempty"`
	Client    string     `json:"client,omitempty"`
	UserAgent string     `json:"userAgent,omitempty"`
	Creator   string     `json:"creator"`
	Protocol  string     `json:"protocol"`
	Host      string     `json:"host"`
	Server    string     `json:"server,omitempty"`
	Code      string     `json:"code"`
	Event     string     `json:"event"`
	Decision  string     `json:"decision"`
	Profile   string     `json:"profile"`
	Text      string     `json:"text"`
	Count     int        `json:"count"`
	First     time.Time  `json:"first"`
	Last      time.Time  `json:"last"`
	Fixed     *time.Time `json:"fixed"`
}

// network answers what the project's proxy recorded, or what it recorded for one agent with ?agent=: the groups of denials, the
// most recently seen first, and the latest events, newest first.
func (s *Server) network(w http.ResponseWriter, r *http.Request, user store.User) {
	view, err := s.hub.Network(r.Context(), user, r.PathValue("project"), r.URL.Query().Get("agent"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	denials := make([]denialJSON, 0, len(view.Denials))
	for _, d := range view.Denials {
		row := denialJSON{Instance: d.Instance, Agent: d.Agent, Claimed: d.ClaimedAgent, Client: d.Client, UserAgent: d.UserAgent, Creator: n.actor(d.Actor), Protocol: d.Protocol, Host: d.Host, Server: d.Server,
			Code: d.Code, Event: d.Event, Decision: d.Decision, Profile: d.Profile, Text: d.Text, Count: d.Count, First: d.First, Last: d.Last}
		if !d.Fixed.IsZero() {
			fixed := d.Fixed
			row.Fixed = &fixed
		}
		denials = append(denials, row)
	}
	events := make([]networkEventJSON, 0, len(view.Events))
	for i := len(view.Events) - 1; i >= 0; i-- { // newest first
		e := view.Events[i]
		row := networkEventJSON{Time: e.Time, Conn: e.Conn, Protocol: string(e.Protocol), Event: string(e.Kind), Decision: string(e.Decision),
			Mode: string(e.Mode), Agent: e.Agent, Instance: e.Instance, Profile: e.Profile, Claimed: e.ClaimedAgent, Client: e.Client,
			UserAgent: e.UserAgent, Host: e.Host, Service: e.Service, Server: e.Server, Method: e.Method,
			Path: e.Path, RawPath: e.RawPath, Status: e.Status, Injected: e.Injected, Substituted: e.Substituted, Command: e.Command, BytesUp: e.BytesUp, BytesDown: e.BytesDown, DurationMs: e.DurationMs}
		if e.Reason != nil {
			row.Reason = &reasonJSON{Code: string(e.Reason.Code), Text: e.Reason.Text}
		}
		events = append(events, row)
	}
	body := map[string]any{"feed": feedOf(view.Feed), "denials": denials, "events": events}
	if view.Ignored > 0 {
		body["ignored"] = map[string]any{"count": view.Ignored, "last": view.IgnoredWhy}
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, user store.User) {
	out, err := s.hub.Logs(r.Context(), user, r.PathValue("project"), r.PathValue("agent"), 400)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": out})
}

func (s *Server) spec(w http.ResponseWriter, r *http.Request, user store.User) {
	spec, err := s.hub.Spec(r.Context(), user, r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": string(spec)})
}

// --- members and audit -------------------------------------------------------------------------------------------------------

func (s *Server) members(w http.ResponseWriter, r *http.Request, user store.User) {
	project := r.PathValue("project")
	members, err := s.hub.Members(r.Context(), user, project)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var candidates []store.User
	if user.IsOperator {
		candidates, _ = s.hub.Candidates(r.Context(), user, project)
	}
	type member struct {
		User userJSON  `json:"user"`
		Role rbac.Role `json:"role"`
	}
	out := struct {
		Members    []member    `json:"members"`
		Candidates []userJSON  `json:"candidates"`
		Roles      []rbac.Role `json:"roles"`
		Me         string      `json:"me"`
	}{Members: []member{}, Candidates: []userJSON{}, Roles: rbac.Roles, Me: user.ID}
	for _, m := range members {
		out.Members = append(out.Members, member{User: userOf(m.User), Role: m.Role})
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
	err := s.hub.SetRole(r.Context(), user, r.PathValue("project"), body.User, role)
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
	entries, err := s.hub.Audit(r.Context(), user, r.PathValue("project"), 100)
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
		n[u.ID] = u.Username
	}
	return n
}

func (n namer) actor(actor string) string {
	switch {
	case actor == "" || actor == rbac.CLI:
		return "the CLI"
	case actor == "hub":
		return "the hub"
	}
	if id, ok := rbac.UserOf(actor); ok {
		if name, ok := n[id]; ok {
			return name
		}
		return "an unknown user"
	}
	return actor
}

// --- users -------------------------------------------------------------------------------------------------------------------

type userRowJSON struct {
	userJSON
	CreatedAt   time.Time      `json:"createdAt"`
	LastLoginAt *time.Time     `json:"lastLoginAt"`
	HasPassword bool           `json:"hasPassword"`
	Identities  []identityJSON `json:"identities"`
	Projects    int            `json:"projects"` // the projects they connected, which their quota is measured against
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, user store.User) {
	users, err := s.hub.Users(r.Context(), user)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	identities, err := s.hub.Store().Identities(r.Context(), "")
	if err != nil {
		s.problem(w, r, err)
		return
	}
	byUser := map[string][]store.Identity{}
	for _, i := range identities {
		byUser[i.UserID] = append(byUser[i.UserID], i)
	}
	local := s.hub.SignIn().LocalLogin
	rows := make([]userRowJSON, 0, len(users))
	for _, u := range users {
		row := userRowJSON{userJSON: userOf(u.User), CreatedAt: u.CreatedAt, HasPassword: u.HasPassword && local, Identities: s.identitiesOf(byUser[u.ID]), Projects: u.Projects}
		if !u.LastLoginAt.IsZero() {
			row.LastLoginAt = &u.LastLoginAt
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": rows, "me": user.ID})
}

// updateUser makes a user an operator or takes it away ({"isOperator"}), disables or enables them ({"disabled"}), or sets their quota
// ({"projectQuota"}): one at a time.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, user store.User) {
	var body struct {
		IsOperator   *bool `json:"isOperator"`
		Disabled     *bool `json:"disabled"`
		ProjectQuota *int  `json:"projectQuota"`
	}
	if !decode(w, r, &body) {
		return
	}
	said := 0
	for _, given := range []bool{body.IsOperator != nil, body.Disabled != nil, body.ProjectQuota != nil} {
		if given {
			said++
		}
	}
	if said != 1 {
		s.apiFail(w, http.StatusBadRequest, "bad_request", "Say one of isOperator, disabled or projectQuota.")
		return
	}
	var err error
	switch {
	case body.IsOperator != nil:
		err = s.hub.SetOperator(r.Context(), user, r.PathValue("id"), *body.IsOperator)
	case body.ProjectQuota != nil:
		err = s.hub.SetProjectQuota(r.Context(), user, r.PathValue("id"), *body.ProjectQuota)
	default:
		var ended []string
		ended, err = s.hub.SetDisabled(r.Context(), user, r.PathValue("id"), *body.Disabled)
		s.endTerminals(ended)
	}
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// removeIdentity takes an account at a provider from a disabled user, so it can be linked to somebody else.
func (s *Server) removeIdentity(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.hub.RemoveIdentity(r.Context(), user, r.PathValue("id"), r.PathValue("identity")); err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- operators -------------------------------------------------------------------------------------------------------------------

func (s *Server) operatorOnly(next authedHandler) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, user store.User) {
		if !user.IsOperator {
			s.problem(w, r, hub.ErrForbidden)
			return
		}
		next(w, r, user)
	}
}
