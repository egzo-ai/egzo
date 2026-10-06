package control

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
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
	mux.HandleFunc("POST /v1/hooks/{name}", a.authenticatedLimit(maxHookPayload, a.hook))
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
	// A removed agent that is still running, and a name nobody spawned, are not agents of the project.
	if !a.server.knownAgent(agent) {
		return "", false
	}
	expected, err := a.server.agentToken(agent)
	if err != nil || !hmac.Equal([]byte(expected), []byte(token)) {
		return "", false
	}
	return agent, true
}

func (a *agentAPI) authenticated(next func(w http.ResponseWriter, r *http.Request, agent string)) http.HandlerFunc {
	return a.authenticatedLimit(maxBody, next)
}

func (a *agentAPI) authenticatedLimit(limit int64, next func(w http.ResponseWriter, r *http.Request, agent string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, ok := a.identify(r)
		if !ok {
			unauthorized(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next(w, r, agent)
	}
}

func (a *agentAPI) authenticatedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.identify(r); !ok {
			unauthorized(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
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

// maxHookPayload is the largest hook payload read. A tool's output can be large and is of no interest
// here: only the state change matters, and what is recorded is reduced to a few named fields.
const maxHookPayload = 4 << 20

// hook ingests a harness hook payload. The hook always drives the agent's activity; it is recorded as an
// event, reduced to what the stream is for, within the agent's budget.
func (a *agentAPI) hook(w http.ResponseWriter, r *http.Request, agent string) {
	name := r.PathValue("name")
	if !safeName.MatchString(name) {
		http.Error(w, "invalid hook name", http.StatusBadRequest)
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookPayload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if a.server.takeBudget(agent, "hook", time.Now()) {
		event := Event{Type: "hook", Agent: agent, Actor: "agent:" + agent, Text: name, Data: summarizeHook(name, payload)}
		if _, err := a.server.events.append(event); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := a.server.onHook(agent, name, payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hookFields are the only parts of a hook payload that are recorded: what happened, never what a tool
// returned or what the user typed (those can hold anything the agent read, secrets included).
var hookFields = []string{"tool_name", "message", "hook_event_name", "source", "reason", "notification_type"}

// announcement matches the lines control composes (see announceLine): fixed words and message ids.
var announcement = regexp.MustCompile(`^(check )?egzo messages? m[0-9a-f]{32}`)

func summarizeHook(name string, payload []byte) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return nil
	}
	summary := map[string]string{}
	for _, field := range hookFields {
		var text string
		if raw, ok := fields[field]; ok && json.Unmarshal(raw, &text) == nil && text != "" {
			if len(text) > 500 {
				text = strings.ToValidUTF8(text[:500], "") + "…"
			}
			summary[field] = text
		}
	}
	// The one prompt worth recording is what egzo itself typed: the line announcing a message. Whatever a
	// person typed is theirs, and may hold anything.
	var prompt string
	if raw, ok := fields["prompt"]; ok && json.Unmarshal(raw, &prompt) == nil && announcement.MatchString(prompt) {
		summary["prompt"] = prompt[:min(len(prompt), 2000)]
	}
	if len(summary) == 0 {
		return nil
	}
	data, _ := json.Marshal(summary)
	return data
}

// The verbs below are shared by the HTTP API and the MCP tools.

func (s *server) reportStatus(agent, text string) error {
	if !s.takeBudget(agent, "status", time.Now()) {
		return tooMany("status lines")
	}
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
