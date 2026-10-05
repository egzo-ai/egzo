package control

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
)

const (
	// AgentPort is where agents reach the control sidecar, as http://control:7777.
	AgentPort   = "7777"
	maxBody     = 64 << 10
	maxTextSize = 16 << 10
)

// agentAPI is what agents see: a few verbs, authenticated by the agent's own token, so an agent
// can only ever act as itself. Operator verbs are not served here at all. The same verbs are
// offered as plain HTTP and as MCP tools.
type agentAPI struct {
	server *server
}

func (a *agentAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/status", a.authenticated(a.status))
	mux.HandleFunc("GET /v1/messages", a.authenticated(a.list))
	mux.HandleFunc("POST /v1/messages", a.authenticated(a.send))
	mux.HandleFunc("GET /v1/messages/{id}", a.authenticated(a.get))
	mux.HandleFunc("POST /v1/messages/{id}/resolve", a.authenticated(a.resolve))
	mux.HandleFunc("POST /v1/messages/{id}/update", a.authenticated(a.update))
	mux.HandleFunc("POST /v1/messages/{id}/ask", a.authenticated(a.ask))
	mux.HandleFunc("GET /v1/agents", a.authenticated(a.agents))
	mux.HandleFunc("POST /v1/hooks/{name}", a.authenticated(a.hook))
	mux.HandleFunc("POST /v1/activity", a.authenticated(a.activity))
	mux.HandleFunc("POST /v1/claim", a.authenticated(a.claim))
	mux.Handle("/mcp", a.authenticatedHandler(a.mcpHandler()))
	return mux
}

// identify checks HTTP basic credentials, the agent name and its token, and returns the agent.
func (a *agentAPI) identify(r *http.Request) (string, bool) {
	agent, token, ok := r.BasicAuth()
	if !ok || !safeName.MatchString(agent) {
		return "", false
	}
	key, err := a.server.projectKey()
	if err != nil {
		return "", false
	}
	if !hmac.Equal([]byte(AgentToken(key, agent)), []byte(token)) {
		return "", false
	}
	return agent, true
}

func (a *agentAPI) authenticated(next func(w http.ResponseWriter, r *http.Request, agent string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, ok := a.identify(r)
		if !ok {
			unauthorized(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next(w, r, agent)
	}
}

func (a *agentAPI) authenticatedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.identify(r); !ok {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="egzo"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func validText(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= maxTextSize
}

// fail writes an error with the status it maps to.
func fail(w http.ResponseWriter, err error) {
	var failure *apiError
	if errors.As(err, &failure) {
		http.Error(w, failure.Text, failure.Status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		http.Error(w, "expected a JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *agentAPI) status(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !validText(body.Text) {
		http.Error(w, "text must not be empty or longer than 16 KB", http.StatusBadRequest)
		return
	}
	if err := a.server.reportStatus(agent, body.Text); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) list(w http.ResponseWriter, r *http.Request, agent string) {
	reply(w, http.StatusOK, a.server.listFor(agent))
}

func (a *agentAPI) send(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		To   string `json:"to"`
		Text string `json:"text"`
		Re   string `json:"re"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id, err := a.server.sendFromAgent(agent, body.To, body.Text, body.Re)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *agentAPI) get(w http.ResponseWriter, r *http.Request, agent string) {
	message, err := a.server.fetch(agent, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, http.StatusOK, message)
}

func (a *agentAPI) resolve(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		Text    string `json:"text"`
		Outcome string `json:"outcome"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if _, err := a.server.resolve(agent, r.PathValue("id"), body.Text, body.Outcome); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) update(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id, err := a.server.update(agent, r.PathValue("id"), body.Text)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *agentAPI) ask(w http.ResponseWriter, r *http.Request, agent string) {
	var body struct {
		Text    string   `json:"text"`
		Choices []string `json:"choices"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id, err := a.server.ask(agent, r.PathValue("id"), body.Text, body.Choices)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *agentAPI) agents(w http.ResponseWriter, r *http.Request, agent string) {
	reply(w, http.StatusOK, a.server.othersOf(agent))
}

// hook ingests a harness hook payload as an event, so hooks and tool calls share one stream.
func (a *agentAPI) hook(w http.ResponseWriter, r *http.Request, agent string) {
	name := r.PathValue("name")
	if !safeName.MatchString(name) {
		http.Error(w, "invalid hook name", http.StatusBadRequest)
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	event := Event{Type: "hook", Agent: agent, Actor: "agent:" + agent, Text: name}
	if json.Valid(payload) {
		event.Data = payload
	}
	if _, err := a.server.events.append(event); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.server.onHook(agent, name, payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The verbs below are shared by the HTTP API and the MCP tools.

func (s *server) reportStatus(agent, text string) error {
	_, err := s.events.append(Event{Type: "status", Agent: agent, Actor: "agent:" + agent, Text: text})
	return err
}

// othersOf lists the other agents of the project with what the control sidecar knows of each.
func (s *server) othersOf(agent string) []AgentStatus {
	out := []AgentStatus{}
	for _, status := range s.agentStatuses() {
		if status.Agent != agent {
			out = append(out, status)
		}
	}
	return out
}

// agentStatuses is every agent of the project, known from `up` and from what they reported.
func (s *server) agentStatuses() []AgentStatus {
	latest := s.events.statuses()
	for _, name := range s.projectAgents() {
		if _, ok := latest[name]; !ok {
			latest[name] = AgentStatus{Agent: name}
		}
	}
	out := make([]AgentStatus, 0, len(latest))
	for name, status := range latest {
		status.Agent = name
		status.Open, status.Waiting = s.overlay(name)
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}
