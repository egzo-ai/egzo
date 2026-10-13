package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Credentials are what an engine needs besides its address. They are sealed in the database and never shown again.
type Credentials struct {
	CA     []byte `json:"ca,omitempty"`
	Cert   []byte `json:"cert,omitempty"`
	Key    []byte `json:"key,omitempty"`
	SSHKey []byte `json:"ssh_key,omitempty"`
}

// Config is one engine as the registry connects it.
type Config struct {
	Name, Host, SSHUser, HostKey string
	Credentials                  Credentials
}

// Connector opens an engine from its config.
type Connector func(ctx context.Context, cfg Config) (Engine, error)

// DockerConnector opens a real engine: a unix socket, tcp (with TLS when a CA or certificate is given), or ssh.
func DockerConnector(ctx context.Context, cfg Config) (Engine, error) {
	target := engine.Target{Host: cfg.Host, CA: cfg.Credentials.CA, Cert: cfg.Credentials.Cert, Key: cfg.Credentials.Key}
	var transport *SSHTransport
	if IsSSH(cfg.Host) {
		var err error
		if transport, err = NewSSHTransport(cfg.Host, cfg.SSHUser, cfg.Credentials.SSHKey, cfg.HostKey); err != nil {
			return nil, err
		}
		target.Host = "tcp://" + cfg.Name + ".ssh"
		target.DialContext = transport.DialContext
	}
	client, err := engine.Dial(ctx, target)
	if err != nil {
		if transport != nil {
			transport.Close()
			if hostKey := hostKeyErrorOf(err); hostKey != nil {
				return nil, hostKey
			}
		}
		return nil, err
	}
	docker := NewDocker(client)
	if transport != nil {
		docker.closers = append(docker.closers, func() { transport.Close() })
	}
	return docker, nil
}

func hostKeyErrorOf(err error) *HostKeyError {
	var hostKey *HostKeyError
	if errors.As(err, &hostKey) {
		return hostKey
	}
	return nil
}

// ProbeSSH connects to an ssh engine without a pinned key and returns the host key it presents, for the operator to confirm.
func ProbeSSH(ctx context.Context, cfg Config) (string, error) {
	transport, err := NewSSHTransport(cfg.Host, cfg.SSHUser, cfg.Credentials.SSHKey, "")
	if err != nil {
		return "", err
	}
	defer transport.Close()
	return transport.Test(ctx)
}

// Status is what the UI shows about one engine.
type Status struct {
	Name, Host string
	Up         bool
	Err        string
	Since      time.Time
}

// Registry holds the engines of the hub. They are data in the database; it connects them on first use and again after a
// failure, never more than once in retryAfter, so one engine being down does not slow the others.
type Registry struct {
	store      *store.Store
	box        *secretbox.Box
	connect    Connector
	retryAfter time.Duration
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	mu     sync.Mutex // one connection attempt at a time
	engine Engine
	err    error
	failed time.Time
	since  time.Time
}

// NewRegistry builds the registry over the database; connect is DockerConnector except in tests.
func NewRegistry(s *store.Store, box *secretbox.Box, connect Connector) *Registry {
	return &Registry{store: s, box: box, connect: connect, retryAfter: 5 * time.Second, now: time.Now, entries: map[string]*entry{}}
}

// Seal encrypts credentials for the database.
func (r *Registry) Seal(name string, creds Credentials) ([]byte, error) {
	data, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	return r.box.Seal("engine:"+name, data)
}

func (r *Registry) config(ctx context.Context, name string) (Config, error) {
	row, err := r.store.Engine(ctx, name)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Name: row.Name, Host: row.Host, SSHUser: row.SSHUser, HostKey: row.HostKey}
	if len(row.Credentials) > 0 {
		plain, err := r.box.Open("engine:"+name, row.Credentials)
		if err != nil {
			return Config{}, fmt.Errorf("engine %s: %w", name, err)
		}
		if err := json.Unmarshal(plain, &cfg.Credentials); err != nil {
			return Config{}, fmt.Errorf("engine %s: %w", name, err)
		}
	}
	return cfg, nil
}

func (r *Registry) entry(name string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[name]
	if e == nil {
		e = &entry{}
		r.entries[name] = e
	}
	return e
}

// Get returns the connected engine, connecting it when needed. An engine that failed lately is not tried again at once.
func (r *Registry) Get(ctx context.Context, name string) (Engine, error) {
	e := r.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engine != nil {
		return e.engine, nil
	}
	if e.err != nil && r.now().Sub(e.failed) < r.retryAfter {
		return nil, e.err
	}
	cfg, err := r.config(ctx, name)
	if err != nil {
		return nil, err
	}
	connected, err := r.connect(ctx, cfg)
	if err != nil {
		e.err, e.failed = err, r.now()
		return nil, err
	}
	e.engine, e.err, e.since = connected, nil, r.now()
	return connected, nil
}

// Failed is called when a call on an engine fails in a way that says the connection is gone: the next Get connects again.
func (r *Registry) Failed(name string, err error) {
	e := r.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engine != nil {
		e.engine.Close()
		e.engine = nil
	}
	e.err, e.failed = err, r.now()
}

// Forget drops the connection of an engine whose settings changed or that was removed.
func (r *Registry) Forget(name string) {
	r.mu.Lock()
	e := r.entries[name]
	delete(r.entries, name)
	r.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engine != nil {
		e.engine.Close()
	}
}

// Statuses lists every engine and whether it answers, trying the ones not connected.
func (r *Registry) Statuses(ctx context.Context) ([]Status, error) {
	rows, err := r.store.Engines(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]Status, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Status{Name: row.Name, Host: row.Host}
			if _, err := r.Get(ctx, row.Name); err != nil {
				s.Err = err.Error()
			} else {
				s.Up = true
				s.Since = r.entry(row.Name).since
			}
			statuses[i] = s
		}()
	}
	wg.Wait()
	return statuses, nil
}

// Close closes every connection.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.engine != nil {
			e.engine.Close()
		}
	}
	r.entries = map[string]*entry{}
}
