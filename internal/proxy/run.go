package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/egzo-ai/egzo/internal/operator"
)

// Config says where the proxy keeps its files inside its container.
type Config struct {
	Listen       string
	CADir        string // private volume: the CA key lives here and nowhere else
	PubDir       string // shared volume: what agents may read
	SystemBundle string
	Socket       string // operator API
}

func DefaultConfig() Config {
	return Config{
		Listen:       ":3128",
		CADir:        "/ca-private",
		PubDir:       "/ca-pub",
		SystemBundle: "/etc/ssl/certs/ca-certificates.crt",
		Socket:       "/run/egzo/operator.sock",
	}
}

// Run creates the CA if needed, publishes it for agents, then serves the proxy and the operator API
// until SIGTERM or SIGINT. Audit lines go to audit (the container's stdout).
func Run(cfg Config, audit io.Writer) error {
	ca, err := LoadOrCreateCA(cfg.CADir)
	if err != nil {
		return fmt.Errorf("CA: %w", err)
	}
	if err := ca.Publish(cfg.PubDir, cfg.SystemBundle); err != nil {
		return fmt.Errorf("publish CA: %w", err)
	}
	server := NewServer(ca, NewAudit(audit))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	listener := &http.Server{Addr: cfg.Listen, Handler: server, ReadHeaderTimeout: 10 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- listener.ListenAndServe() }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		listener.Shutdown(shutdown)
	}()

	operatorDone := make(chan error, 1)
	go func() { operatorDone <- operator.Serve(cfg.Socket, operatorHandler(server, ca)) }()

	select {
	case err := <-failed:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-operatorDone:
		return err
	}
}

// operatorHandler is the proxy's operator API: load a policy, report which one is loaded.
func operatorHandler(server *Server, ca *CA) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"hash": server.PolicyHash(), "ca": ca.Fingerprint()})
	})
	mux.HandleFunc("PUT /policy", func(w http.ResponseWriter, r *http.Request) {
		var policy Policy
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&policy); err != nil {
			http.Error(w, "invalid policy: "+err.Error(), http.StatusBadRequest)
			return
		}
		if policy.Hash == "" {
			http.Error(w, "a policy needs a hash", http.StatusBadRequest)
			return
		}
		server.SetPolicy(&policy)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
