package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// server holds the control sidecar's state on its volume. It is the operator API: reached only
// through `engine exec`, never from the network.
type server struct {
	dir string
	mu  sync.Mutex
}

func newServer(dir string) *server { return &server{dir: dir} }

func ensureState(dir string) error { return os.MkdirAll(dir, 0o755) }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /tokens/{agent}", s.token)
	mux.HandleFunc("PUT /specs/{hash}", s.putSpec)
	mux.HandleFunc("GET /specs/{hash}", s.getSpec)
	mux.HandleFunc("GET /specs", s.listSpecs)
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
