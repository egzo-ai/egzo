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
	"time"
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
	rateMu     sync.Mutex
	sends      map[string][]time.Time
	buckets    map[string]*bucket
	// sendMu makes the checks of a send and its write one step.
	sendMu sync.Mutex
	key    []byte // the project key, read once
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
	mux.HandleFunc("POST /messages", s.enqueue)
	mux.HandleFunc("GET /messages", s.listMessages)
	mux.HandleFunc("GET /messages/{id}", s.getMessage)
	mux.HandleFunc("POST /messages/{id}/resolve", s.resolveForOperator)
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
	if s.key != nil {
		return s.key, nil
	}
	path := filepath.Join(s.dir, "key")
	key, err := os.ReadFile(path)
	switch {
	case err == nil && len(key) == 32:
		s.key = key
		return key, nil
	case err == nil:
		// Never replace it: a new key would change every token without anyone being told.
		return nil, fmt.Errorf("the project key %s is damaged (%d bytes, want 32): restore it, or delete it to issue new tokens", path, len(key))
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	// Written under a temporary name and then linked into place: the link fails when another sidecar on
	// the same volume got there first, so exactly one key is ever created, and it is never half written.
	temp, err := os.CreateTemp(s.dir, "key.*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(fresh); err != nil {
		temp.Close()
		return nil, err
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temp.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		winner, err := os.ReadFile(path)
		if err != nil || len(winner) != 32 {
			return nil, fmt.Errorf("the project key %s is damaged", path)
		}
		s.key = winner
		return winner, nil
	}
	s.key = fresh
	return fresh, nil
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

// maxSpecSize bounds one snapshot, and maxSpecs how many are kept.
const (
	maxSpecSize = 4 << 20
	maxSpecs    = 50
)

func (s *server) putSpec(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	path, ok := s.specPath(hash)
	if !ok {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSpecSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "the snapshot is larger than the limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != hash {
		http.Error(w, "the snapshot is not the one this hash names", http.StatusBadRequest)
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	temp, err := os.CreateTemp(dir, "spec.*.tmp")
	if err == nil {
		defer os.Remove(temp.Name())
		_, err = temp.Write(body)
		if closeErr := temp.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = os.Chmod(temp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	retireSpecs(dir, maxSpecs)
	w.WriteHeader(http.StatusNoContent)
}

// retireSpecs keeps the newest `keep` snapshots.
func retireSpecs(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type snapshot struct {
		name string
		time time.Time
	}
	var all []snapshot
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && strings.HasSuffix(entry.Name(), ".yaml") {
			all = append(all, snapshot{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].time.After(all[j].time) })
	for i := keep; i < len(all); i++ {
		os.Remove(filepath.Join(dir, all[i].name))
	}
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

// enqueue is a person sending a request to an agent: the CLI, and later the hub for its users.
func (s *server) enqueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To        string `json:"to"`
		From      string `json:"from"`
		Text      string `json:"text"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		http.Error(w, "expected {\"to\": \"agent:name\", \"text\": message}", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "operator"
	}
	if !actorName.MatchString(body.From) || !isHuman(body.From) {
		http.Error(w, "from must be operator or user:<id>", http.StatusBadRequest)
		return
	}
	message, event, err := s.send(sendRequest{From: body.From, To: body.To, Text: body.Text, Interrupt: body.Interrupt})
	if err != nil {
		fail(w, err)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": message.ID, "seq": event.Seq})
}

// listMessages is `egzo messages`: the open requests and questions, or with all=1 everything. agent=
// narrows it to the messages an agent sent or received, kind= to one kind.
func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	agent, kind, all := query.Get("agent"), query.Get("kind"), query.Get("all") == "1"
	messages := s.events.selectMessages(func(m Message) bool {
		if agent != "" && m.To != "agent:"+agent && m.From != "agent:"+agent {
			return false
		}
		if kind != "" && m.Kind != kind {
			return false
		}
		if all {
			return true
		}
		return (m.Kind == kindRequest || m.Kind == kindQuestion) && m.State != stateResolved
	})
	json.NewEncoder(w).Encode(messages)
}

func (s *server) getMessage(w http.ResponseWriter, r *http.Request) {
	message, ok := s.events.message(r.PathValue("id"))
	if !ok {
		http.Error(w, errNoSuchMessage.Text, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(message)
}

// resolveForOperator is a person answering a question or closing a request addressed to them.
func (s *server) resolveForOperator(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Actor   string `json:"actor"`
		Text    string `json:"text"`
		Outcome string `json:"outcome"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		http.Error(w, "expected {\"text\": answer, \"outcome\": done|declined|failed}", http.StatusBadRequest)
		return
	}
	if body.Actor == "" {
		body.Actor = "operator"
	}
	if body.Outcome == "" {
		body.Outcome = "done"
	}
	if !actorName.MatchString(body.Actor) || !isHuman(body.Actor) {
		http.Error(w, "invalid actor", http.StatusBadRequest)
		return
	}
	if _, err := s.resolveForPerson(body.Actor, r.PathValue("id"), body.Text, body.Outcome); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) agents(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.agentStatuses())
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

// projectAgents lists the agents the project said it has, or none before it has.
func (s *server) projectAgents() []string {
	data, err := os.ReadFile(filepath.Join(s.dir, "project.json"))
	if err != nil {
		return nil
	}
	var project struct {
		Agents []string `json:"agents"`
	}
	if json.Unmarshal(data, &project) != nil {
		return nil
	}
	return project.Agents
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
