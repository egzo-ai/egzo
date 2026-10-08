// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/signal"
	"regexp"
	"sync"
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
	go func() { operatorDone <- operator.Serve(cfg.Socket, operatorHandler(server, ca, cfg)) }()

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

// agentName is what an instance may be called (the identifier rule of the project file).
var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// operatorHandler is the proxy's operator API: load a policy, report which one is loaded, bind agents
// to its profiles.
func operatorHandler(server *Server, ca *CA, cfg Config) http.Handler {
	mux := http.NewServeMux()
	var mu sync.Mutex // the CA in use, and rotating it
	current := ca
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fingerprint := current.Fingerprint()
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"hash": server.PolicyHash(), "ca": fingerprint})
	})
	mux.HandleFunc("POST /ca/rotate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		rotated, err := rotate(cfg, current)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		server.SetCA(rotated)
		current = rotated
		json.NewEncoder(w).Encode(map[string]string{"ca": rotated.Fingerprint()})
	})
	mux.HandleFunc("GET /agents", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(server.BoundAgents())
	})
	mux.HandleFunc("PUT /agents/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !agentName.MatchString(name) {
			http.Error(w, "invalid agent name", http.StatusBadRequest)
			return
		}
		var binding Binding
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&binding); err != nil {
			http.Error(w, "expected {\"token\": ..., \"profile\": ...}: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := server.BindAgent(name, binding); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /agents/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !agentName.MatchString(name) {
			http.Error(w, "invalid agent name", http.StatusBadRequest)
			return
		}
		server.UnbindAgent(name)
		w.WriteHeader(http.StatusNoContent)
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

// rotate makes a new CA and switches to it in an order that leaves the proxy consistent when a step
// fails: publish for the agents first, store on disk second, and put the old one back if storing fails.
// Nothing is changed in memory until both have worked.
func rotate(cfg Config, current *CA) (*CA, error) {
	next, err := NewCA()
	if err != nil {
		return nil, err
	}
	if err := next.Publish(cfg.PubDir, cfg.SystemBundle); err != nil {
		current.Publish(cfg.PubDir, cfg.SystemBundle)
		return nil, fmt.Errorf("publish the new CA: %w", err)
	}
	if err := next.Save(cfg.CADir); err != nil {
		current.Publish(cfg.PubDir, cfg.SystemBundle)
		return nil, fmt.Errorf("store the new CA: %w", err)
	}
	return next, nil
}
