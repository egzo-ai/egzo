package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// server holds the control sidecar's state on its volume. It is the operator API: reached only
// through `engine exec`, never from the network.
type server struct {
	dir    string
	mu     sync.Mutex
	events *store
	// deliveryMu makes activity changes, claims and acknowledgements one at a time, so two of them
	// never decide from the same state.
	deliveryMu sync.Mutex
}

func newServer(dir string) (*server, error) {
	events, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	return &server{dir: dir, events: events}, nil
}

func ensureState(dir string) error { return os.MkdirAll(dir, 0o755) }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /tokens/{agent}", s.token)
	mux.HandleFunc("PUT /specs/{hash}", s.putSpec)
	mux.HandleFunc("GET /specs/{hash}", s.getSpec)
	mux.HandleFunc("GET /specs", s.listSpecs)
	mux.HandleFunc("GET /events", s.streamEvents)
	mux.HandleFunc("POST /queue", s.enqueue)
	mux.HandleFunc("GET /queue", s.queue)
	mux.HandleFunc("GET /questions", s.listQuestions)
	mux.HandleFunc("POST /questions/{id}/answer", s.answer)
	mux.HandleFunc("GET /agents", s.agents)
	mux.HandleFunc("PUT /project", s.putProject)
	return mux
}

// projectKey is a random key created on first use. Per-agent tokens are derived from it, so they
// are stable across `up` runs for as long as the control volume exists, without anything being
// stored per agent.
func (s *server) projectKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "key")
	if key, err := os.ReadFile(path); err == nil && len(key) == 32 {
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// AgentToken derives the token that identifies an agent to the proxy and the control sidecar.
func AgentToken(key []byte, agent string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("agent:" + agent))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *server) token(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !safeName.MatchString(agent) {
		http.Error(w, "invalid agent name", http.StatusBadRequest)
		return
	}
	key, err := s.projectKey()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	io.WriteString(w, AgentToken(key, agent))
}

func (s *server) specPath(hash string) (string, bool) {
	if !safeName.MatchString(hash) {
		return "", false
	}
	return filepath.Join(s.dir, "specs", hash+".yaml"), true
}

func (s *server) putSpec(w http.ResponseWriter, r *http.Request) {
	path, ok := s.specPath(r.PathValue("hash"))
	if !ok {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getSpec(w http.ResponseWriter, r *http.Request) {
	path, ok := s.specPath(r.PathValue("hash"))
	if !ok {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func (s *server) listSpecs(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "specs"))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	sort.Strings(names)
	io.WriteString(w, strings.Join(names, "\n"))
	if len(names) > 0 {
		io.WriteString(w, "\n")
	}
}

var actorName = regexp.MustCompile(`^(operator|(user|agent):[A-Za-z0-9][A-Za-z0-9._@-]{0,127})$`)

// streamEvents writes the event stream as JSON lines; with follow=1 it stays open for new events.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	agent := r.URL.Query().Get("agent")
	follow := r.URL.Query().Get("follow") == "1"
	flusher, _ := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	for {
		events, wake := s.events.since(after, agent)
		for _, event := range events {
			encoder.Encode(event)
			after = event.Seq
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !follow {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		}
	}
}

func (s *server) enqueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To        string `json:"to"`
		From      string `json:"from"`
		Text      string `json:"text"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil || !safeName.MatchString(body.To) {
		http.Error(w, "expected {\"to\": agent, \"text\": message}", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "operator"
	}
	if !actorName.MatchString(body.From) || strings.TrimSpace(body.Text) == "" || len(body.Text) > maxTextSize {
		http.Error(w, "invalid sender or text", http.StatusBadRequest)
		return
	}
	id := newID("m")
	if _, err := s.events.append(Event{Type: "message", Agent: body.To, Actor: body.From, ID: id, Text: body.Text}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if body.Interrupt {
		if _, err := s.events.append(Event{Type: "interrupt", Agent: body.To, Actor: body.From}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (s *server) queue(w http.ResponseWriter, r *http.Request) {
	messages := s.events.unfinished(r.URL.Query().Get("agent"))
	if messages == nil {
		messages = []Message{}
	}
	json.NewEncoder(w).Encode(messages)
}

func (s *server) listQuestions(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.events.questions())
}

func (s *server) answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Actor string `json:"actor"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "expected {\"text\": answer}", http.StatusBadRequest)
		return
	}
	if body.Actor == "" {
		body.Actor = "operator"
	}
	if !actorName.MatchString(body.Actor) {
		http.Error(w, "invalid actor", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	for _, question := range s.events.questions() {
		if question.ID != id {
			continue
		}
		if question.Answered {
			http.Error(w, "question already answered", http.StatusConflict)
			return
		}
		if _, err := s.events.append(Event{Type: "answer", Agent: question.Agent, Actor: body.Actor, ID: id, Text: body.Text}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

func (s *server) agents(w http.ResponseWriter, r *http.Request) {
	statuses := s.events.statuses()
	if statuses == nil {
		statuses = []AgentStatus{}
	}
	json.NewEncoder(w).Encode(statuses)
}

// putProject records which agents the project has, so an agent cannot hand work to one that does not exist.
func (s *server) putProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agents []string `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		http.Error(w, "expected {\"agents\": [...]}", http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(body)
	if err := os.WriteFile(filepath.Join(s.dir, "project.json"), data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// knownAgent reports whether name is an agent of the project. Before the project has said which
// agents it has, any well-formed name is accepted.
func (s *server) knownAgent(name string) bool {
	data, err := os.ReadFile(filepath.Join(s.dir, "project.json"))
	if err != nil {
		return safeName.MatchString(name)
	}
	var project struct {
		Agents []string `json:"agents"`
	}
	if json.Unmarshal(data, &project) != nil {
		return safeName.MatchString(name)
	}
	for _, agent := range project.Agents {
		if agent == name {
			return true
		}
	}
	return false
}

// handoff queues a message for another agent, as the calling agent.
func (s *server) handoff(from, to, text string) error {
	switch {
	case to == from:
		return errors.New("an agent cannot hand work to itself")
	case !s.knownAgent(to):
		return fmt.Errorf("no agent %q in this project", to)
	case !validText(text):
		return errors.New("text must not be empty or longer than 16 KB")
	}
	_, err := s.events.append(Event{Type: "message", Agent: to, Actor: "agent:" + from, ID: newID("m"), Text: text})
	return err
}
