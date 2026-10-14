package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/oidctest"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/web"
)

type rig struct {
	t        *testing.T
	server   *httptest.Server
	provider *httptest.Server
	hub      *hub.Hub
	store    *store.Store
	fakes    map[string]*fake.Engine
}

var ctx = context.Background()

func newRig(t *testing.T, engineNames ...string) *rig {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := secretbox.New("0123456789abcdef-secret")
	r := &rig{t: t, store: s, fakes: map[string]*fake.Engine{}}
	registry := engines.NewRegistry(s, box, func(_ context.Context, cfg engines.Config) (engines.Engine, error) {
		f, ok := r.fakes[cfg.Name]
		if !ok {
			f = fake.New()
			r.fakes[cfg.Name] = f
		}
		return f, nil
	})
	registry.SetProbe(func(context.Context, engines.Config) (string, error) { return "SHA256:probed-key", nil })
	r.hub = hub.New(s, registry, nil)
	for _, name := range engineNames {
		r.fakes[name] = fake.New()
		s.PutEngine(ctx, store.EngineRow{Name: name, Host: "unix:///" + name})
	}
	provider := oidctest.New("", "hub", "secret")
	r.provider = httptest.NewServer(provider.Handler())
	provider.Issuer = r.provider.URL
	t.Cleanup(r.provider.Close)
	urls, _ := publicurl.New("", nil)
	app, err := web.New(r.hub, auth.New(auth.Config{Issuer: r.provider.URL, ClientID: "hub", ClientSecret: "secret"}), urls, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app.UseApp(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>egzo hub</title>")}, "assets/app-1234.js": {Data: []byte("//js")}})
	r.server = httptest.NewServer(app.Handler())
	t.Cleanup(r.server.Close)
	return r
}

// browser is an http client with cookies, as a person's browser; it sends the Origin header a real one sends on a form.
type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func (r *rig) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: r.t, base: r.server.URL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

// json is one JSON object, as the front end receives it.
type json_ map[string]any

// call makes an API call as the front end does: JSON in and out, the Origin of the hub's own page on a change.
func (b *browser) call(method, path string, body any) (int, json_) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = strings.NewReader(string(data))
	} else if method != "GET" {
		reader = strings.NewReader("{}")
	}
	req, _ := http.NewRequest(method, b.base+"/api/v1"+path, reader)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", b.base)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out json_
	if err := json.Unmarshal(data, &out); err != nil && len(data) > 0 {
		b.t.Fatalf("%s %s answered %d with something that is not JSON: %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode, out
}

func (b *browser) get(path string) (int, json_)            { return b.call("GET", path, nil) }
func (b *browser) post(path string, body any) (int, json_) { return b.call("POST", path, body) }
func (b *browser) del(path string) (int, json_)            { return b.call("DELETE", path, nil) }

// page fetches an address of the app itself, as a browser navigating to it.
func (b *browser) page(path string) (int, string) {
	b.t.Helper()
	resp, err := b.client.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// at digs into a decoded answer: at(m, "a", "b") is m["a"]["b"].
func at(m json_, path ...string) any {
	var cur any = map[string]any(m)
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	return cur
}

func list(m json_, key string) []map[string]any {
	var out []map[string]any
	items, _ := m[key].([]any)
	for _, it := range items {
		if o, ok := it.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

func names(items []map[string]any, key string) []string {
	var out []string
	for _, it := range items {
		out = append(out, it[key].(string))
	}
	return out
}

// signIn walks the login: the hub redirects to the provider, the person types a name, the provider redirects back.
func (b *browser) signIn(user string) {
	b.t.Helper()
	noFollow := *b.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Get(b.base + "/login/start")
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("login start = %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	q := authURL.Query()
	resp, err = noFollow.PostForm(authURL.Scheme+"://"+authURL.Host+"/authorize", url.Values{
		"user": {user}, "client_id": q["client_id"], "redirect_uri": q["redirect_uri"], "state": q["state"], "nonce": q["nonce"],
	})
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	resp, err = b.client.Get(b.base + back.RequestURI())
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("callback ended on %d", resp.StatusCode)
	}
}

func (r *rig) userID(name string) string {
	r.t.Helper()
	users, _ := r.store.Users(ctx)
	for _, u := range users {
		if u.Name == name {
			return u.ID
		}
	}
	r.t.Fatalf("no user %s", name)
	return ""
}

func project(name string, templates ...string) engines.Project {
	return engines.Project{Name: name, Templates: templates}
}

func agent(name, actor string) engines.Agent {
	return engines.Agent{Name: name, Template: "coder", State: "running", Activity: "idle", Actor: actor, Status: "reading the code", Created: time.Now()}
}
