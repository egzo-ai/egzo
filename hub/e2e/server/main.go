// Command server is the hub with fake projects linked to it and a test login provider, for the browser tests (hub/e2e). It is not shipped.
//
// It prints one JSON line with the addresses it listens on, then serves until killed:
//
//	hub      the hub's web UI
//	control  a small JSON API the tests use to change the world: POST /control {"op": ...}
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/oidctest"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/web"
	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/proxy"
)

func main() {
	scenario := flag.String("scenario", "multi", "multi (two machines) or single")
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
	h := hub.New(db, nil, hub.FeedOptions{Silent: 5 * time.Second})
	ctx := context.Background()
	if err := h.RecoverTasks(ctx); err != nil {
		return err
	}

	hubListener, providerListener, controlListener := listen(), listen(), listen()
	issuer := "http://" + providerListener.Addr().String()
	provider := oidctest.New(issuer, "hub", "secret")
	go http.Serve(providerListener, provider.Handler())

	// the starter account signs in with this password; everyone else through the test provider
	if _, err := h.SetupSignIn(ctx, hub.SignInOptions{LocalLogin: true, AdminPassword: "e2e-admin-password", Issuer: issuer}); err != nil {
		return err
	}
	urls, _ := publicurl.New("", nil)
	address := "http://" + hubListener.Addr().String()
	app, err := web.New(h, auth.New(auth.Config{Issuer: issuer, ClientID: "hub", ClientSecret: "secret"}), web.Options{SSOName: "Test login", LinkURL: address}, urls, nil)
	if err != nil {
		return err
	}
	go http.Serve(hubListener, app.Handler())
	go h.Run(ctx)

	w := &world{hub: h, db: db, address: address, projects: map[string]*fake.Engine{}}
	machines := []string{"local"}
	if scenario == "multi" {
		machines = append(machines, "lab")
	}
	for _, machine := range machines {
		if err := w.seed(machine); err != nil {
			return err
		}
	}
	go http.Serve(controlListener, control(w, h, db))

	json.NewEncoder(os.Stdout).Encode(map[string]string{
		"hub": address, "provider": issuer, "control": "http://" + controlListener.Addr().String(),
	})
	select {}
}

// world holds the fake projects, by machine and name.
type world struct {
	hub      *hub.Hub
	db       *store.Store
	address  string
	projects map[string]*fake.Engine
}

// dial is a project's link reaching the hub with a code, or with its key alone: it waits until the hub holds the connection or has told
// it to go.
func (w *world) dial(f *fake.Engine, code string) {
	before := len(f.Verdicts())
	go w.hub.Link(context.Background(), f.Hello(), code, f.Connect())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (len(f.Verdicts()) == before || f.Verdicts()[len(f.Verdicts())-1].State == hublink.StateApproved && !f.Served()) {
		time.Sleep(time.Millisecond)
	}
}

// add registers a project, with a code of the starter account.
func (w *world) add(machine, project string, prepare func(*fake.Engine), templates ...string) (*fake.Engine, error) {
	f := fake.New(project, machine, templates...)
	if prepare != nil {
		prepare(f)
	}
	w.projects[machine+"/"+project] = f
	ctx := context.Background()
	admin, err := w.db.UserByName(ctx, store.StarterUsername)
	if err != nil {
		return f, err
	}
	_, code, err := w.hub.CreateLinkCode(ctx, admin)
	if err != nil {
		return f, err
	}
	w.dial(f, code)
	return f, nil
}

func (w *world) seed(machine string) error {
	e, err := w.add(machine, "web", nil, "coder", "reviewer")
	if err != nil {
		return err
	}
	now := time.Now()
	e.SetAgent(links.Agent{Name: "coder-1", ID: newInstanceID(), Template: "coder", State: "running", Activity: "working", Actor: "operator",
		Status: "refactoring the parser on " + machine, Created: now})
	e.SetAgent(links.Agent{Name: "reviewer-1", Template: "reviewer", State: "exited", Activity: "stopped", Actor: "operator", Created: now})
	e.AddEvent(links.Event{Seq: 1, Time: now, Type: "status", Agent: "coder-1", Actor: "agent:coder-1", Text: "refactoring the parser on " + machine})
	other, template := "data", "analyst"
	if machine != "local" {
		other, template = "ml", "trainer"
	}
	_, err = w.add(machine, other, nil, template)
	return err
}

func (w *world) get(machine, project string) *fake.Engine {
	if machine == "" {
		machine = "local"
	}
	return w.projects[machine+"/"+project]
}

type command struct {
	Op      string `json:"op"`
	Machine string `json:"machine"` // "local" when empty
	Project string `json:"project"`
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Role    string `json:"role"`
	State   string `json:"state"`
	Text    string `json:"text"`
	Waiting bool   `json:"waiting"`
	Actor   string `json:"actor"`
	// an event of the proxy's audit trail (op audit): the agent's, to host, with a decision and a reason code
	Host     string `json:"host"`
	Decision string `json:"decision"`
	Code     string `json:"code"`
	Mode     string `json:"mode"`
	Service  string `json:"service"`
	// the requests of the connection, after its connect
	Requests []auditRequest `json:"requests"`
}

// auditRequest is a request inside a terminated connection: whose credential went with it, or why none did.
type auditRequest struct {
	Path     string `json:"path"`
	Injected string `json:"injected"`
	Code     string `json:"code"`
	Text     string `json:"text"`
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
		e := w.get(c.Machine, c.Project)
		idOf := func() string {
			reg, err := db.RegistrationByKey(ctx, e.Hello().Key)
			if err != nil {
				return ""
			}
			return reg.ID
		}
		switch c.Op {
		case "id":
			json.NewEncoder(rw).Encode(idOf())
			return
		case "down":
			e.Drop()
		case "up":
			w.dial(e, "")
		case "register": // `egzo project register`: a project that dials the hub for the first time, with the code a person gave it
			machine := orDefault(c.Machine, "local")
			f := fake.New(c.Project, machine, "coder")
			w.projects[machine+"/"+c.Project] = f
			w.dial(f, c.Code)
		case "verdict": // what the link was told last
			verdicts := e.Verdicts()
			json.NewEncoder(rw).Encode(verdicts[len(verdicts)-1])
			return
		case "grant":
			users, _ := db.Users(ctx)
			granted := false
			for _, u := range users {
				if u.Username == c.User {
					db.SetRole(ctx, idOf(), u.ID, rbac.Role(c.Role))
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
			id := newInstanceID() // the same agent keeps its id; a new one gets its own
			if a, ok := agentOf(e, c.Agent); ok {
				id = a.ID
			}
			e.SetAgent(links.Agent{Name: c.Agent, ID: id, Template: "coder", State: container, Activity: orDefault(c.State, "idle"),
				Status: c.Text, Waiting: c.Waiting, Actor: actor, Created: time.Now()})
		case "audit":
			a, ok := agentOf(e, c.Agent)
			if !ok {
				http.Error(rw, "no agent "+c.Agent, http.StatusNotFound)
				return
			}
			event := links.AuditEvent{Conn: connID(), Protocol: proxy.HTTPS, Kind: proxy.KindConnect, Decision: proxy.Outcome(c.Decision),
				Mode: proxy.Mode(c.Mode), Agent: a.Name, Instance: a.ID, Profile: "default", ClaimedAgent: a.Name, Client: "10.89.3.2",
				UserAgent: "curl/8.11.1", Host: c.Host, Service: c.Service}
			if c.Code == string(proxy.CodeCredentials) { // only claimed: nothing proved who it was
				event.Agent, event.Instance, event.Profile = "", "", ""
			}
			if c.Code != "" {
				event.Reason = &proxy.Reason{Code: proxy.Code(c.Code), Text: orDefault(c.Text, c.Code)}
			}
			event.Time = time.Now().UTC()
			events := []links.AuditEvent{event}
			for i, q := range c.Requests {
				request := event
				request.Time, request.Kind, request.Decision, request.Mode, request.Service = event.Time.Add(time.Duration(i+1)*time.Millisecond), proxy.KindRequest, proxy.OutcomeAllow, "", ""
				request.Method, request.Path, request.Status, request.Injected, request.Reason = "GET", q.Path, 200, q.Injected, nil
				if q.Code != "" {
					request.Reason = &proxy.Reason{Code: proxy.Code(q.Code), Text: q.Text}
				}
				events = append(events, request)
			}
			e.AddAudit(events...)
		case "add-project":
			// state "silent": its control sidecar logs no status, as an old one does
			if _, err := w.add(orDefault(c.Machine, "local"), c.Project, func(f *fake.Engine) { f.SetFeedSilent(c.State == "silent") }, "coder"); err != nil {
				http.Error(rw, err.Error(), http.StatusInternalServerError)
				return
			}
		case "unlink":
			e.Unlink()
		case "hold": // spawns, starts, stops and removals wait until release, as ones that take long do
			e.Hold()
		case "release":
			e.Release()
		case "fail-next": // the next spawn, start, stop or removal fails, saying c.State
			e.FailNext(c.State)
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

func agentOf(e *fake.Engine, name string) (links.Agent, bool) {
	for _, a := range e.Agents() {
		if a.Name == name {
			return a, true
		}
	}
	return links.Agent{}, false
}

// newInstanceID makes an id of the form instances have: core's is a ULID.
func newInstanceID() string {
	const letters = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw := make([]byte, 26)
	rand.Read(raw)
	for i, b := range raw {
		raw[i] = letters[int(b)%len(letters)]
	}
	return string(raw)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// connID is a connection's id as the proxy makes them: 64 random bits in hex.
func connID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}
