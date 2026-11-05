// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/egzo-ai/egzo/internal/engine"
)

// helperEngine is just enough of the Docker API for file helpers: volumes, and containers that mount them, are made, start and go.
type helperEngine struct {
	mu         sync.Mutex
	volumes    map[string]map[string]string // name -> labels
	containers map[string]*helperContainer  // id -> container
	names      map[string]string            // name -> id
	next       int
	removed    []string // the names of the containers removed
	lists      int      // ContainerList calls
	// startDelay holds a start, out of the lock, as a slow engine would.
	startDelay time.Duration
	// onVolumeRemove runs before a volume is removed, out of the lock.
	onVolumeRemove func(name string)
}

type helperContainer struct {
	id, name, state string
	created         time.Time
	labels          map[string]string
	volumes         []string
}

func newHelperEngine(t *testing.T) (*engine.Client, *helperEngine) {
	t.Helper()
	fake := &helperEngine{volumes: map[string]map[string]string{}, containers: map[string]*helperContainer{}, names: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	return &engine.Client{API: api}, fake
}

func (f *helperEngine) addVolume(project string, target FilesTarget) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := target.Volume(project)
	f.volumes[name] = engine.Identity{Project: project, Service: target.Workspace, Kind: kindWorkspace, Workspace: target.Workspace, Instance: target.Instance}.Labels()
	return name
}

// addContainer is a container made by something else: a file helper in a state, or an instance that mounts the volume.
func (f *helperEngine) addContainer(name, state string, created time.Time, labels map[string]string, volumes ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := "id" + strconv.Itoa(f.next)
	f.containers[id] = &helperContainer{id: id, name: name, state: state, created: created, labels: labels, volumes: volumes}
	f.names[name] = id
	return id
}

func (f *helperEngine) find(key string) *helperContainer {
	if id, ok := f.names[key]; ok {
		key = id
	}
	return f.containers[key]
}

func (f *helperEngine) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.45")
	reply := func(code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != nil {
			json.NewEncoder(w).Encode(body)
		}
	}
	if path == "/_ping" {
		reply(200, nil)
		return
	}
	if strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start") {
		f.mu.Lock()
		delay := f.startDelay
		f.mu.Unlock()
		time.Sleep(delay)
	}
	if strings.HasPrefix(path, "/volumes/") && r.Method == http.MethodDelete {
		f.mu.Lock()
		hook := f.onVolumeRemove
		f.mu.Unlock()
		if hook != nil {
			hook(strings.TrimPrefix(path, "/volumes/"))
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(path, "/images/"):
		reply(200, map[string]any{"Id": "sha256:abc"})
	case strings.HasPrefix(path, "/volumes/") && r.Method == http.MethodGet:
		name := strings.TrimPrefix(path, "/volumes/")
		labels, ok := f.volumes[name]
		if !ok {
			reply(404, map[string]string{"message": "no such volume"})
			return
		}
		reply(200, map[string]any{"Name": name, "Labels": labels})
	case strings.HasPrefix(path, "/volumes/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(path, "/volumes/")
		for _, c := range f.containers {
			for _, v := range c.volumes {
				if v == name {
					reply(409, map[string]string{"message": "remove " + name + ": volume is in use - [" + c.id + "]"})
					return
				}
			}
		}
		delete(f.volumes, name)
		reply(204, nil)
	case path == "/containers/json":
		f.lists++
		args, _ := filters.FromJSON(r.URL.Query().Get("filters"))
		list := []map[string]any{}
		for _, c := range f.containers {
			if !matches(c, args) {
				continue
			}
			var mounts []map[string]string
			for _, v := range c.volumes {
				mounts = append(mounts, map[string]string{"Type": "volume", "Name": v})
			}
			list = append(list, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Labels": c.labels, "State": c.state, "Mounts": mounts})
		}
		reply(200, list)
	case path == "/containers/create":
		name := r.URL.Query().Get("name")
		if _, taken := f.names[name]; taken {
			reply(409, map[string]string{"message": "Conflict. The container name is already in use"})
			return
		}
		var body struct {
			Labels     map[string]string
			HostConfig struct{ Mounts []struct{ Source string } }
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.next++
		id := "id" + strconv.Itoa(f.next)
		c := &helperContainer{id: id, name: name, state: "created", created: time.Now(), labels: body.Labels}
		for _, m := range body.HostConfig.Mounts {
			c.volumes = append(c.volumes, m.Source)
		}
		f.containers[id], f.names[name] = c, id
		reply(201, map[string]any{"Id": id})
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
		c := f.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start"))
		if c == nil {
			reply(404, map[string]string{"message": "No such container"})
			return
		}
		c.state = "running"
		reply(204, nil)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		c := f.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json"))
		if c == nil {
			reply(404, map[string]string{"message": "No such container"})
			return
		}
		reply(200, map[string]any{"Id": c.id, "Name": "/" + c.name, "Created": c.created.Format(time.RFC3339Nano),
			"State": map[string]any{"Status": c.state, "Running": c.state == "running"}, "Config": map[string]any{"Labels": c.labels}})
	case strings.HasPrefix(path, "/containers/") && r.Method == http.MethodDelete:
		c := f.find(strings.TrimPrefix(path, "/containers/"))
		if c == nil {
			reply(404, map[string]string{"message": "No such container"})
			return
		}
		delete(f.containers, c.id)
		delete(f.names, c.name)
		f.removed = append(f.removed, c.name)
		reply(204, nil)
	default:
		reply(404, map[string]string{"message": "not faked: " + r.Method + " " + path + "?" + r.URL.RawQuery})
	}
}

// matches says whether c passes the label and volume filters of a list.
func matches(c *helperContainer, args filters.Args) bool {
	for _, label := range args.Get("label") {
		key, value, _ := strings.Cut(label, "=")
		if c.labels[key] != value {
			return false
		}
	}
	for _, volume := range args.Get("volume") {
		found := false
		for _, v := range c.volumes {
			found = found || v == volume
		}
		if !found {
			return false
		}
	}
	return true
}

func helperEnv(context.Context) (PrepEnv, error) {
	return PrepEnv{Image: "egzo:test", User: "1000:1000"}, nil
}

func TestConnectionsThatMakeAHelperAtOnceShareItAndRemoveNothing(t *testing.T) {
	c, fake := newHelperEngine(t)
	target := FilesTarget{Workspace: "docs"}
	fake.addVolume("p", target)
	fake.startDelay = 300 * time.Millisecond
	ids := make([]string, 4)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = FileHelper(context.Background(), c, helperEnv, "p", target)
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Errorf("connection %d: %q %v, want %q", i, ids[i], errs[i], ids[0])
		}
	}
	if len(fake.removed) != 0 {
		t.Errorf("removed %v: a helper being started by another connection is waited for", fake.removed)
	}
}

func TestAHelperMadeAndNeverStartedIsReplaced(t *testing.T) {
	c, fake := newHelperEngine(t)
	target := FilesTarget{Workspace: "docs"}
	volume := fake.addVolume("p", target)
	stale := fake.addContainer(fileHelperName(volume), "created", time.Now().Add(-time.Hour), nil, volume)
	id, err := FileHelper(context.Background(), c, helperEnv, "p", target)
	if err != nil || id == "" || id == stale {
		t.Fatalf("%q %v", id, err)
	}
	if len(fake.removed) != 1 {
		t.Errorf("removed %v", fake.removed)
	}
}

func workspaceVolumes(volumes ...Resource) Observed {
	for i := range volumes {
		volumes[i].Type, volumes[i].Kind, volumes[i].ID = "volume", kindWorkspace, volumes[i].Name
	}
	return Observed{Resources: volumes}
}

func helperLabels(project string) map[string]string {
	return engine.Identity{Project: project, Service: kindFiles, Kind: kindFiles}.Labels()
}

func TestARefusedWorkspaceRmLeavesTheFileHelpersAlone(t *testing.T) {
	c, fake := newHelperEngine(t)
	base, worktree := BaseVolume("p", "repo"), WorktreeVolume("p", "repo", "x")
	fake.addVolume("p", FilesTarget{Workspace: "repo", Instance: "x"})
	fake.addContainer(fileHelperName(worktree), "running", time.Now(), helperLabels("p"), worktree)
	observed := workspaceVolumes(Resource{Name: base, Workspace: "repo"}, Resource{Name: worktree, Workspace: "repo", Instance: "x"})
	err := RemoveWorkspaces(context.Background(), c, observed, "p", []string{"repo"}, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--worktree x") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.removed) != 0 {
		t.Errorf("removed %v though nothing was deleted", fake.removed)
	}
}

func TestWorkspaceRmTakesTheFileHelpersWithTheVolume(t *testing.T) {
	c, fake := newHelperEngine(t)
	volume := fake.addVolume("p", FilesTarget{Workspace: "docs"})
	fake.addContainer(fileHelperName(volume), "running", time.Now(), helperLabels("p"), volume)
	err := RemoveWorkspaces(context.Background(), c, workspaceVolumes(Resource{Name: volume, Workspace: "docs"}), "p", []string{"docs"}, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, left := fake.volumes[volume]; left || len(fake.removed) != 1 {
		t.Errorf("volume left %v, removed %v", left, fake.removed)
	}
}
