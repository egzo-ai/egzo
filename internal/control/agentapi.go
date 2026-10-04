package control

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
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
	mux.HandleFunc("POST /v1/say", a.authenticated(a.say))
	mux.HandleFunc("POST /v1/ask", a.authenticated(a.ask))
	mux.HandleFunc("GET /v1/questions/{id}", a.authenticated(a.question))
	mux.HandleFunc("GET /v1/inbox", a.authenticated(a.inbox))
	mux.HandleFunc("POST /v1/hooks/{name}", a.authenticated(a.hook))
	mux.HandleFunc("POST /v1/activity", a.authenticated(a.activity))
	mux.HandleFunc("POST /v1/claim", a.authenticated(a.claim))
	mux.HandleFunc("POST /v1/ack", a.authenticated(a.ack))
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

func readText(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "expected JSON like {\"text\": \"...\"}", http.StatusBadRequest)
		return "", false
	}
	if !validText(body.Text) {
		http.Error(w, "text must not be empty or longer than 16 KB", http.StatusBadRequest)
		return "", false
	}
	return body.Text, true
}

func validText(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= maxTextSize
}

func (a *agentAPI) status(w http.ResponseWriter, r *http.Request, agent string) {
	text, ok := readText(w, r)
	if !ok {
		return
	}
	if err := a.server.reportStatus(agent, text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) say(w http.ResponseWriter, r *http.Request, agent string) {
	text, ok := readText(w, r)
	if !ok {
		return
	}
	if err := a.server.say(agent, text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) ask(w http.ResponseWriter, r *http.Request, agent string) {
	text, ok := readText(w, r)
	if !ok {
		return
	}
	id, err := a.server.ask(agent, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// question lets an agent see the answer to a question it asked, and only its own.
func (a *agentAPI) question(w http.ResponseWriter, r *http.Request, agent string) {
	if question, ok := a.server.questionFor(agent, r.PathValue("id")); ok {
		json.NewEncoder(w).Encode(question)
		return
	}
	http.NotFound(w, r)
}

// inbox hands an agent its queued messages and marks them delivered.
func (a *agentAPI) inbox(w http.ResponseWriter, r *http.Request, agent string) {
	messages, err := a.server.takeInbox(agent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(messages)
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

func newID(prefix string) string {
	raw := make([]byte, 4)
	rand.Read(raw)
	return prefix + hex.EncodeToString(raw)
}

// The verbs below are shared by the HTTP API and the MCP tools.

func (s *server) reportStatus(agent, text string) error {
	_, err := s.events.append(Event{Type: "status", Agent: agent, Actor: "agent:" + agent, Text: text})
	return err
}

func (s *server) say(agent, text string) error {
	_, err := s.events.append(Event{Type: "say", Agent: agent, Actor: "agent:" + agent, Text: text})
	return err
}

func (s *server) ask(agent, text string) (string, error) {
	id := newID("q")
	_, err := s.events.append(Event{Type: "question", Agent: agent, Actor: "agent:" + agent, ID: id, Text: text})
	return id, err
}

// questionFor returns a question only to the agent that asked it.
func (s *server) questionFor(agent, id string) (Question, bool) {
	for _, question := range s.events.questions() {
		if question.ID == id && question.Agent == agent {
			return question, true
		}
	}
	return Question{}, false
}

// takeInbox returns an agent's undelivered messages and marks them delivered.
func (s *server) takeInbox(agent string) ([]Message, error) {
	messages := s.events.pending(agent)
	for _, message := range messages {
		if _, err := s.events.append(Event{Type: "delivered", Agent: agent, Actor: "agent:" + agent, ID: message.ID}); err != nil {
			return nil, err
		}
	}
	if messages == nil {
		messages = []Message{}
	}
	return messages, nil
}
