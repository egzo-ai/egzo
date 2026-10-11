// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/egzo-ai/egzo/internal/engine"
)

// fakeEngine is just enough of the Docker API for the lock: containers created under a name, which is unique.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]map[string]string // id -> labels
	names      map[string]string            // name -> id
	next       int
}

func newFakeEngine(t *testing.T) (*engine.Client, *fakeEngine) {
	t.Helper()
	fake := &fakeEngine{containers: map[string]map[string]string{}, names: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	return &engine.Client{API: api}, fake
}

func (f *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.45")
	reply := func(code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(body)
	}
	switch {
	case strings.HasPrefix(path, "/images/") && r.Method == http.MethodGet:
		reply(200, map[string]any{"Id": "sha256:abc"})
	case path == "/containers/create":
		name := r.URL.Query().Get("name")
		if _, taken := f.names[name]; taken {
			reply(409, map[string]string{"message": "Conflict. The container name is already in use"})
			return
		}
		var body struct{ Labels map[string]string }
		json.NewDecoder(r.Body).Decode(&body)
		f.next++
		id := "id" + strconv.Itoa(f.next)
		f.containers[id], f.names[name] = body.Labels, id
		reply(201, map[string]any{"Id": id})
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		id, ok := f.names[key]
		if !ok {
			id = key
		}
		labels, found := f.containers[id]
		if !found {
			reply(404, map[string]string{"message": "No such container: " + key})
			return
		}
		reply(200, map[string]any{"Id": id, "Config": map[string]any{"Labels": labels}})
	case strings.HasPrefix(path, "/containers/") && r.Method == http.MethodDelete:
		key := strings.TrimPrefix(path, "/containers/")
		id, ok := f.names[key]
		if !ok {
			id = key
		}
		if _, found := f.containers[id]; !found {
			reply(404, map[string]string{"message": "No such container: " + key})
			return
		}
		delete(f.containers, id)
		for name, owner := range f.names {
			if owner == id {
				delete(f.names, name)
			}
		}
		w.WriteHeader(204)
	default:
		reply(404, map[string]string{"message": "not faked: " + r.Method + " " + path})
	}
}

func (f *fakeEngine) holds(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.names[name]
	return ok
}

func TestOnlyOneCommandChangesAProjectAtATime(t *testing.T) {
	c, fake := newFakeEngine(t)
	ctx := context.Background()
	release, err := Lock(ctx, c, "proj", "egzo:test")
	if err != nil {
		t.Fatal(err)
	}
	if !fake.holds("proj.lock") {
		t.Fatal("the lease is not a container named <project>.lock")
	}
	_, err = Lock(ctx, c, "proj", "egzo:test")
	var locked *LockedError
	if !asLocked(err, &locked) || !strings.Contains(err.Error(), "another egzo command") || !strings.Contains(err.Error(), "egzo project unlock") {
		t.Fatalf("a second lock on the same project: %v", err)
	}
	if other, err := Lock(ctx, c, "other", "egzo:test"); err != nil {
		t.Errorf("another project was blocked: %v", err)
	} else {
		other()
	}
	release()
	if fake.holds("proj.lock") {
		t.Fatal("the lease was not removed on release")
	}
	again, err := Lock(ctx, c, "proj", "egzo:test")
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

func TestLockWaitWaitsForTheHolderAndGivesUpAfterTheTimeout(t *testing.T) {
	c, _ := newFakeEngine(t)
	ctx := context.Background()
	release, err := Lock(ctx, c, "proj", "egzo:test")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := LockWait(ctx, c, "proj", "egzo:test", 400*time.Millisecond); err == nil || time.Since(started) < 350*time.Millisecond {
		t.Fatalf("a held lock was not waited for: err = %v after %s", err, time.Since(started))
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()
	got, err := LockWait(ctx, c, "proj", "egzo:test", 5*time.Second)
	if err != nil {
		t.Fatalf("the lock was not taken once released: %v", err)
	}
	got()
}

func TestAnExpiredLeaseIsTakenOverAndItsHolderCannotRemoveTheNewOne(t *testing.T) {
	c, fake := newFakeEngine(t)
	ctx := context.Background()
	fake.names["proj.lock"], fake.containers["old"] = "old", map[string]string{
		engine.LabelLockHolder: "long gone", engine.LabelLockExpires: "1",
	}
	release, err := Lock(ctx, c, "proj", "egzo:test")
	if err != nil {
		t.Fatalf("an expired lease was not taken over: %v", err)
	}
	if fake.names["proj.lock"] == "old" {
		t.Fatal("the old lease is still the lock")
	}
	release()
	if fake.holds("proj.lock") {
		t.Error("the lease was not released")
	}
}

func TestALiveLeaseNamesItsHolder(t *testing.T) {
	c, fake := newFakeEngine(t)
	fake.names["proj.lock"], fake.containers["held"] = "held", map[string]string{
		engine.LabelLockHolder: "egzo 0.3.0, pid 77 on elsewhere", engine.LabelLockExpires: strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}
	_, err := Lock(context.Background(), c, "proj", "egzo:test")
	if err == nil || !strings.Contains(err.Error(), "pid 77 on elsewhere") {
		t.Errorf("err = %v", err)
	}
}

func TestUnlockRemovesTheLeaseWhoeverHoldsItAndSaysWho(t *testing.T) {
	c, fake := newFakeEngine(t)
	ctx := context.Background()
	if _, wasLocked, err := Unlock(ctx, c, "proj"); err != nil || wasLocked {
		t.Fatalf("nothing to unlock: %v %v", wasLocked, err)
	}
	fake.names["proj.lock"], fake.containers["held"] = "held", map[string]string{
		engine.LabelLockHolder: "killed-1", engine.LabelLockExpires: strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}
	holder, wasLocked, err := Unlock(ctx, c, "proj")
	if err != nil || !wasLocked || holder != "killed-1" || fake.holds("proj.lock") {
		t.Errorf("holder = %q, locked = %v, err = %v", holder, wasLocked, err)
	}
}

func TestATakerThatSawAnExpiredLeaseCannotRemoveTheOneAnotherTakerMadeSince(t *testing.T) {
	c, fake := newFakeEngine(t)
	ctx := context.Background()
	fake.names["proj.lock"], fake.containers["old"] = "old", map[string]string{engine.LabelLockHolder: "dead", engine.LabelLockExpires: "1"}
	// B read the expired lease (id "old") ...
	oldID, _, _, found, err := readLease(ctx, c, "proj")
	if err != nil || !found || oldID != "old" {
		t.Fatalf("read = %q %v %v", oldID, found, err)
	}
	// ... then A took over ...
	release, err := Lock(ctx, c, "proj", "egzo:test")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// ... and B removes what it read, which is gone: A's lease must stand.
	c.API.ContainerRemove(ctx, oldID, container.RemoveOptions{Force: true})
	if !fake.holds("proj.lock") {
		t.Fatal("removing the expired lease by its id took the live one")
	}
	if _, err := Lock(ctx, c, "proj", "egzo:test"); err == nil {
		t.Fatal("a second command took a lock that is held")
	}
}
