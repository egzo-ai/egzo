package hub

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var engineName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// EngineInput is what the operator types to add or change an engine. Credentials are write-only: a blank field keeps what is
// stored on an update, and nothing ever shows them again.
type EngineInput struct {
	Name, Host, SSHUser   string
	CA, Cert, Key, SSHKey string
	// PinHostKey is the ssh host key fingerprint the operator confirmed.
	PinHostKey string
}

// HostKeyPending is returned when an ssh engine's host key must be confirmed by the operator before it is pinned.
type HostKeyPending struct{ Fingerprint string }

func (e *HostKeyPending) Error() string {
	return "confirm the ssh host key " + e.Fingerprint + " to pin it"
}

func (h *Hub) requireOperator(ctx context.Context, user store.User, action, engine string) error {
	if user.IsOperator {
		return nil
	}
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: action, Engine: engine, Detail: "refused: only an operator may do that"})
	return ErrForbidden
}

func validateHost(host string) error {
	u, err := url.Parse(host)
	if err != nil || (u.Scheme != "unix" && u.Scheme != "tcp" && u.Scheme != "ssh") {
		return fmt.Errorf("the address must start with unix://, tcp:// or ssh://")
	}
	if u.Scheme == "unix" && u.Path == "" {
		return fmt.Errorf("a unix address needs a socket path: unix:///var/run/docker.sock")
	}
	if u.Scheme != "unix" && u.Hostname() == "" {
		return fmt.Errorf("the address needs a host")
	}
	return nil
}

// SaveEngine adds an engine, or changes it when the name exists. The connection is tested before anything is kept; an ssh host key
// that is not pinned yet comes back as *HostKeyPending for the operator to confirm.
func (h *Hub) SaveEngine(ctx context.Context, user store.User, in EngineInput) error {
	if err := h.requireOperator(ctx, user, "engine.save", in.Name); err != nil {
		return err
	}
	in.Name, in.Host = strings.TrimSpace(in.Name), strings.TrimSpace(in.Host)
	if !engineName.MatchString(in.Name) {
		return errors.New("the name is lowercase letters, digits, - and _ (up to 31), and starts with a letter or digit")
	}
	if err := validateHost(in.Host); err != nil {
		return err
	}
	cfg := engines.Config{Name: in.Name, Host: in.Host, SSHUser: strings.TrimSpace(in.SSHUser), HostKey: in.PinHostKey}
	existing, err := h.store.Engine(ctx, in.Name)
	switch {
	case err == nil:
		if cfg.Credentials, err = h.credentialsOf(ctx, existing); err != nil {
			return err
		}
		if cfg.SSHUser == "" {
			cfg.SSHUser = existing.SSHUser
		}
		if cfg.HostKey == "" && existing.Host == in.Host {
			cfg.HostKey = existing.HostKey
		}
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	for _, f := range []struct {
		value string
		to    *[]byte
	}{{in.CA, &cfg.Credentials.CA}, {in.Cert, &cfg.Credentials.Cert}, {in.Key, &cfg.Credentials.Key}, {in.SSHKey, &cfg.Credentials.SSHKey}} {
		if strings.TrimSpace(f.value) != "" {
			*f.to = []byte(f.value)
		}
	}
	if engines.IsSSH(in.Host) {
		if len(cfg.Credentials.SSHKey) == 0 {
			return errors.New("an ssh engine needs a private key")
		}
		if cfg.HostKey == "" {
			fingerprint, err := h.registry.Probe(ctx, cfg)
			if err != nil {
				return fmt.Errorf("cannot reach %s: %w", in.Host, err)
			}
			return &HostKeyPending{Fingerprint: fingerprint}
		}
	}
	if err := h.registry.Test(ctx, cfg); err != nil {
		return fmt.Errorf("cannot connect to %s: %w", in.Host, err)
	}
	sealed, err := h.registry.Seal(cfg.Name, cfg.Credentials)
	if err != nil {
		return err
	}
	if err := h.store.PutEngine(ctx, store.EngineRow{Name: cfg.Name, Host: cfg.Host, SSHUser: cfg.SSHUser, HostKey: cfg.HostKey, Credentials: sealed}); err != nil {
		return err
	}
	h.registry.Forget(cfg.Name)
	_ = h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "engine.save", Engine: cfg.Name, Detail: cfg.Host, Allowed: true})
	return h.Scan(ctx)
}

func (h *Hub) credentialsOf(ctx context.Context, row store.EngineRow) (engines.Credentials, error) {
	return h.registry.Open(row)
}

// RemoveEngine forgets an engine and the roles on its projects. Nothing on the engine is touched.
func (h *Hub) RemoveEngine(ctx context.Context, user store.User, name string) error {
	if err := h.requireOperator(ctx, user, "engine.remove", name); err != nil {
		return err
	}
	if err := h.store.DeleteEngine(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	h.registry.Forget(name)
	h.mu.Lock()
	delete(h.scanned, name)
	h.mu.Unlock()
	if rows, err := h.store.Engines(ctx); err == nil {
		names := make([]string, len(rows))
		for i, row := range rows {
			names[i] = row.Name
		}
		h.reconcileFeeds(names) // its streams stop now, not at the next listing
	}
	return h.store.Audit(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "engine.remove", Engine: name, Allowed: true})
}

// SeedEngine adds the engine the environment names when the hub knows none, so the quick start works unchanged. It never
// connects: the first scan reports an engine that is not there.
func (h *Hub) SeedEngine(ctx context.Context, name, host string) error {
	rows, err := h.store.Engines(ctx)
	if err != nil || len(rows) > 0 {
		return err
	}
	return h.store.PutEngine(ctx, store.EngineRow{Name: name, Host: host})
}
