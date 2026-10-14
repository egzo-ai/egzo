// Command server is the hub with fake engines and a test login provider, for the browser tests (hub/e2e). It is not shipped.
//
// It prints one JSON line with the addresses it listens on, then serves until killed:
//
//	hub      the hub's web UI
//	control  a small JSON API the tests use to change the world: POST /control {"op": ...}
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/oidctest"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/web"
)

func main() {
	scenario := flag.String("scenario", "multi", "multi (two engines) or single")
	flag.Parse()
	if err := run(*scenario); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func listen() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	return l
}

func run(scenario string) error {
	dir, err := os.MkdirTemp("", "egzo-hub-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		return err
	}
	box, _ := secretbox.New("0123456789abcdef-e2e-secret")
	world := newWorld()
	registry := engines.NewRegistry(db, box, world.connect)
	registry.SetProbe(func(context.Context, engines.Config) (string, error) { return "SHA256:e2e-host-key", nil })
	h := hub.New(db, registry, nil)
	ctx := context.Background()
	names := []string{"local"}
	if scenario == "multi" {
		names = append(names, "lab")
	}
	for _, n := range names {
		world.seed(n)
		if err := db.PutEngine(ctx, store.EngineRow{Name: n, Host: "unix:///" + n + ".sock"}); err != nil {
			return err
		}
	}

	hubListener, providerListener, controlListener := listen(), listen(), listen()
	issuer := "http://" + providerListener.Addr().String()
	provider := oidctest.New(issuer, "hub", "secret")
	go http.Serve(providerListener, provider.Handler())

	urls, _ := publicurl.New("", nil)
	app, err := web.New(h, auth.New(auth.Config{Issuer: issuer, ClientID: "hub", ClientSecret: "secret"}), urls, nil)
	if err != nil {
		return err
	}
	go http.Serve(hubListener, app.Handler())
	go func() {
		for {
			h.Scan(ctx)
			time.Sleep(time.Second)
		}
	}()
	go http.Serve(controlListener, control(world, h, db))

	json.NewEncoder(os.Stdout).Encode(map[string]string{
		"hub": "http://" + hubListener.Addr().String(), "provider": issuer, "control": "http://" + controlListener.Addr().String(),
	})
	select {}
}

// world holds the fake engines, by name.
type world struct {
	engines map[string]*fake.Engine
}

func newWorld() *world { return &world{engines: map[string]*fake.Engine{}} }

func (w *world) get(name string) *fake.Engine {
	if w.engines[name] == nil {
		w.engines[name] = fake.New()
	}
	return w.engines[name]
}

func (w *world) connect(_ context.Context, cfg engines.Config) (engines.Engine, error) {
	return w.get(cfg.Name), nil
}

func (w *world) seed(name string) {
	e := w.get(name)
	now := time.Now()
	e.AddProject(engines.Project{Name: "web", Templates: []string{"coder", "reviewer"}})
	e.SetAgent("web", engines.Agent{Name: "coder-1", Template: "coder", State: "running", Activity: "working", Actor: "operator", Status: "refactoring the parser on " + name, Created: now})
	e.SetAgent("web", engines.Agent{Name: "reviewer-1", Template: "reviewer", State: "exited", Activity: "stopped", Actor: "operator", Created: now})
	e.AddEvent("web", engines.Event{Seq: 1, Time: now, Type: "status", Agent: "coder-1", Actor: "agent:coder-1", Text: "refactoring the parser on " + name})
	if name == "local" {
		e.AddProject(engines.Project{Name: "data", Templates: []string{"analyst"}})
	} else {
		e.AddProject(engines.Project{Name: "ml", Templates: []string{"trainer"}})
	}
}

type command struct {
	Op      string `json:"op"`
	Engine  string `json:"engine"`
	Project string `json:"project"`
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Role    string `json:"role"`
	State   string `json:"state"`
	Text    string `json:"text"`
	Waiting bool   `json:"waiting"`
	Actor   string `json:"actor"`
}

func control(w *world, h *hub.Hub, db *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /control", func(rw http.ResponseWriter, r *http.Request) {
		var c command
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		ctx := r.Context()
		e := w.get(c.Engine)
		switch c.Op {
		case "down":
			e.SetDown(fake.ErrUnreachable)
		case "up":
			e.SetDown(nil)
			h.Registry().Forget(c.Engine)
		case "scan":
			h.Scan(ctx)
		case "grant":
			users, _ := db.Users(ctx)
			granted := false
			for _, u := range users {
				if u.Name == c.User {
					db.SetRole(ctx, c.Engine, c.Project, u.ID, rbac.Role(c.Role))
					granted = true
				}
			}
			if !granted {
				http.Error(rw, "nobody called "+c.User+" has signed in yet", http.StatusNotFound)
				return
			}
		case "set-agent":
			actor := c.Actor
			if actor == "" {
				actor = "operator"
			}
			container := "running"
			if c.State == "stopped" {
				container = "exited"
			}
			e.SetAgent(c.Project, engines.Agent{Name: c.Agent, Template: "coder", State: container, Activity: orDefault(c.State, "idle"),
				Status: c.Text, Waiting: c.Waiting, Actor: actor, Created: time.Now()})
		case "add-project":
			e.AddProject(engines.Project{Name: c.Project, Templates: []string{"coder"}})
		case "remove-project":
			e.RemoveProject(c.Project)
		case "calls":
			json.NewEncoder(rw).Encode(e.Calls())
			return
		default:
			http.Error(rw, "unknown op "+c.Op, 400)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
