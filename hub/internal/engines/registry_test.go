package engines_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

type rig struct {
	registry *engines.Registry
	store    *store.Store
	box      *secretbox.Box
	opened   map[string]int
	fakes    map[string]*fake.Engine
	fail     map[string]error
	seen     engines.Config
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := secretbox.New("0123456789abcdef-secret")
	r := &rig{store: s, box: box, opened: map[string]int{}, fakes: map[string]*fake.Engine{}, fail: map[string]error{}}
	r.registry = engines.NewRegistry(s, box, func(ctx context.Context, cfg engines.Config) (engines.Engine, error) {
		r.seen = cfg
		if err := r.fail[cfg.Name]; err != nil {
			return nil, err
		}
		r.opened[cfg.Name]++
		f := fake.New()
		r.fakes[cfg.Name] = f
		return f, nil
	})
	return r
}

func (r *rig) add(t *testing.T, name string, creds engines.Credentials) {
	t.Helper()
	sealed, err := r.registry.Seal(name, creds)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.PutEngine(context.Background(), store.EngineRow{Name: name, Host: "unix:///x/" + name, Credentials: sealed}); err != nil {
		t.Fatal(err)
	}
}

var ctx = context.Background()

func TestAnEngineIsConnectedOnFirstUseAndKept(t *testing.T) {
	r := newRig(t)
	r.add(t, "local", engines.Credentials{})
	a, err := r.registry.Get(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.registry.Get(ctx, "local")
	if a != b || r.opened["local"] != 1 {
		t.Errorf("opened %d times", r.opened["local"])
	}
	if _, err := r.registry.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown engine: %v", err)
	}
}

func TestCredentialsAreSealedInTheDatabaseAndOpenedToConnect(t *testing.T) {
	r := newRig(t)
	r.add(t, "lab", engines.Credentials{Key: []byte("PRIVATE-KEY-MATERIAL")})
	row, _ := r.store.Engine(ctx, "lab")
	if strings.Contains(string(row.Credentials), "PRIVATE-KEY-MATERIAL") {
		t.Fatal("credentials are stored in the clear")
	}
	if _, err := r.registry.Get(ctx, "lab"); err != nil {
		t.Fatal(err)
	}
	if string(r.seen.Credentials.Key) != "PRIVATE-KEY-MATERIAL" {
		t.Errorf("the connector got %+v", r.seen.Credentials)
	}
	// a different key cannot read them: the failure names the cause
	other, _ := secretbox.New("another-secret-key-here")
	wrong := engines.NewRegistry(r.store, other, func(context.Context, engines.Config) (engines.Engine, error) { return fake.New(), nil })
	if _, err := wrong.Get(ctx, "lab"); err == nil || !strings.Contains(err.Error(), "EGZO_SECRET_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestOneEngineBeingDownDoesNotStopTheOthers(t *testing.T) {
	r := newRig(t)
	r.add(t, "local", engines.Credentials{})
	r.add(t, "lab", engines.Credentials{})
	r.fail["lab"] = errors.New("dial tcp: connection refused")
	statuses, err := r.registry.Statuses(ctx)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("%+v %v", statuses, err)
	}
	by := map[string]engines.Status{}
	for _, s := range statuses {
		by[s.Name] = s
	}
	if !by["local"].Up || by["lab"].Up || !strings.Contains(by["lab"].Err, "connection refused") {
		t.Errorf("statuses = %+v", by)
	}
}

func TestAFailedEngineIsRetriedOnlyAfterTheCooldown(t *testing.T) {
	r := newRig(t)
	r.add(t, "lab", engines.Credentials{})
	r.fail["lab"] = errors.New("down")
	now := time.Unix(1000, 0)
	engines.SetClock(r.registry, func() time.Time { return now })
	attempts := 0
	r.registry = engines.NewRegistry(r.store, r.box, func(context.Context, engines.Config) (engines.Engine, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("down")
		}
		return fake.New(), nil
	})
	engines.SetClock(r.registry, func() time.Time { return now })
	r.registry.Get(ctx, "lab")
	r.registry.Get(ctx, "lab")
	if attempts != 1 {
		t.Fatalf("attempts = %d: retried inside the cooldown", attempts)
	}
	now = now.Add(time.Minute)
	if _, err := r.registry.Get(ctx, "lab"); err != nil || attempts != 2 {
		t.Errorf("after the cooldown: attempts=%d err=%v", attempts, err)
	}
}

func TestFailedAndForgetCloseTheConnection(t *testing.T) {
	r := newRig(t)
	r.add(t, "local", engines.Credentials{})
	r.registry.Get(ctx, "local")
	first := r.fakes["local"]
	r.registry.Failed("local", errors.New("broken pipe"))
	if !first.Closed() {
		t.Error("the broken connection was not closed")
	}
	engines.SetClock(r.registry, func() time.Time { return time.Now().Add(time.Hour) })
	r.registry.Get(ctx, "local")
	if r.fakes["local"] == first {
		t.Error("not reconnected")
	}
	second := r.fakes["local"]
	r.registry.Forget("local")
	if !second.Closed() {
		t.Error("Forget left the connection open")
	}
}
