// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/operator"
	"github.com/egzo-ai/egzo/internal/seal"
)

// Config says where the proxy keeps its files inside its container.
type Config struct {
	Listen       string
	SSHListen    string // where agents sign in to be relayed to an ssh server of their profile
	CADir        string // private volume: the CA key lives here, sealed, and nowhere else
	PubDir       string // shared volume: what agents may read
	SystemBundle string
	Socket       string // operator API
}

func DefaultConfig() Config {
	return Config{
		Listen:       ":3128",
		SSHListen:    ":2222",
		CADir:        "/ca-private",
		PubDir:       "/ca-pub",
		SystemBundle: "/etc/ssl/certs/ca-certificates.crt",
		Socket:       "/run/egzo/operator.sock",
	}
}

// Run publishes for agents what it can of the CA and the ssh host key without the secret key, then serves the proxy and
// the operator API until SIGTERM or SIGINT. The private halves are opened (or made) when a policy brings the secret key.
// Audit lines go to audit (the container's stdout).
func Run(cfg Config, audit io.Writer) error {
	store, err := OpenStore(cfg.CADir)
	if err != nil {
		return fmt.Errorf("CA: %w", err)
	}
	server := NewServer(store.CA(), NewAudit(audit))
	server.UseStore(store)
	if err := publishStore(cfg, server, store, nil); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go server.RenewSignIns(ctx)

	listener := &http.Server{Addr: cfg.Listen, Handler: server, ReadHeaderTimeout: 10 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- listener.ListenAndServe() }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		listener.Shutdown(shutdown)
	}()

	sshListener, err := net.Listen("tcp", cfg.SSHListen)
	if err != nil {
		return fmt.Errorf("ssh listener: %w", err)
	}
	go func() {
		<-ctx.Done()
		sshListener.Close()
	}()
	go server.ServeSSH(sshListener)

	operatorDone := make(chan error, 1)
	go func() { operatorDone <- operator.Serve(cfg.Socket, operatorHandler(server, store, cfg)) }()

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

// publishStore hands the server what the store has opened and publishes its public halves for agents: the CA
// certificate and bundle, and the ssh client files (with the servers of policy, when there is one).
func publishStore(cfg Config, server *Server, store *Store, policy *Policy) error {
	ca := store.CA()
	server.SetCA(ca)
	if ca != nil {
		if err := ca.Publish(cfg.PubDir, cfg.SystemBundle); err != nil {
			return fmt.Errorf("publish CA: %w", err)
		}
	}
	hostKey, public := store.SSHHostKey()
	if hostKey != nil {
		server.SetSSHHostKey(hostKey)
	}
	if public != nil {
		if err := PublishSSHClientFiles(cfg.PubDir, public, policy); err != nil {
			return fmt.Errorf("publish ssh client files: %w", err)
		}
	}
	return nil
}

// sealKeys reads the project and the secret key of a request body (a policy, or a rotation) and derives the keys.
func sealKeys(project, secretKey string) (*seal.Keys, error) {
	if project == "" || secretKey == "" {
		return nil, errors.New("the project and its secret key are required")
	}
	return seal.New([]byte(secretKey), project)
}

// operatorHandler is the proxy's operator API: load a policy, report which one is loaded, bind agents
// to its profiles.
func operatorHandler(server *Server, store *Store, cfg Config) http.Handler {
	mux := http.NewServeMux()
	var mu sync.Mutex // loading a policy and rotating the CA, which both change the store
	var loaded *Policy
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		fingerprint := ""
		if ca := store.CA(); ca != nil {
			fingerprint = ca.Fingerprint()
		}
		json.NewEncoder(w).Encode(map[string]string{"hash": server.PolicyHash(), "ca": fingerprint})
	})
	mux.HandleFunc("POST /ca/rotate", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Project   string `json:"project"`
			SecretKey string `json:"secret_key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request); err != nil {
			http.Error(w, "expected {\"project\": ..., \"secret_key\": ...}: "+err.Error(), http.StatusBadRequest)
			return
		}
		keys, err := sealKeys(request.Project, request.SecretKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		rotated, _, err := store.Rotate(keys, func(ca *CA) error { return ca.Publish(cfg.PubDir, cfg.SystemBundle) })
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := publishStore(cfg, server, store, loaded); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := server.signIns.load(); err != nil {
			http.Error(w, "open the sign-ins: "+err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ca": rotated.Fingerprint()})
	})
	mux.HandleFunc("GET /oauth", func(w http.ResponseWriter, r *http.Request) {
		statuses := map[string]SignInStatus{}
		for name := range server.oauthServices() {
			statuses[name] = server.signIns.status(name)
		}
		json.NewEncoder(w).Encode(statuses)
	})
	mux.HandleFunc("GET /oauth/{service}/stand-ins", func(w http.ResponseWriter, r *http.Request) {
		standIns, ok := server.signIns.standIns(r.PathValue("service"))
		if !ok {
			http.Error(w, "service "+r.PathValue("service")+" has no sign-in", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(standIns)
	})
	mux.HandleFunc("POST /oauth/{service}/logout", func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		oauth, ok := server.oauthServices()[service]
		if !ok {
			http.Error(w, "no OAuth service "+service+" in the loaded policy", http.StatusNotFound)
			return
		}
		if !server.signIns.logout(r.Context(), service, oauth, server.transport().RoundTrip) {
			http.Error(w, "service "+service+" has no sign-in", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /agents", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(server.BoundAgents())
	})
	mux.HandleFunc("PUT /agents/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !names.Instance(name) {
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
		if !names.Instance(name) {
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
		keys, err := sealKeys(policy.Project, policy.SecretKey)
		if err != nil {
			http.Error(w, "invalid policy: "+err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// The store is opened before the policy is loaded: a proxy that cannot open its CA serves nothing new.
		changed, err := store.Unlock(keys)
		if errors.Is(err, ErrWrongSecretKey) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		} else if err != nil {
			http.Error(w, "open the proxy's private volume: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if changed {
			if err := server.signIns.load(); err != nil {
				http.Error(w, "open the sign-ins: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := publishStore(cfg, server, store, &policy); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := server.SetPolicy(&policy); err != nil {
			http.Error(w, "invalid policy: "+err.Error(), http.StatusBadRequest)
			return
		}
		loaded = &policy
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
