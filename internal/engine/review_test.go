package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

// fakeEngine answers the one call waitExit makes: the state of an exec, running for a while.
func fakeEngine(t *testing.T, runningPolls int32, exit int) (*Client, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/exec/e1/json") {
			http.NotFound(w, r)
			return
		}
		running := polls.Add(1) <= runningPolls
		json.NewEncoder(w).Encode(map[string]any{"ID": "e1", "Running": running, "ExitCode": map[bool]int{true: 0, false: exit}[running]})
	}))
	t.Cleanup(server.Close)
	api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	return &Client{API: api}, &polls
}

func TestTheExitCodeIsReadOnlyOnceTheProcessIsGone(t *testing.T) {
	c, polls := fakeEngine(t, 3, 7)
	code, err := c.waitExit(context.Background(), "e1")
	if err != nil || code != 7 {
		t.Fatalf("code = %d, err = %v: the first answers said the command was still running with exit code 0", code, err)
	}
	if polls.Load() < 4 {
		t.Errorf("only %d polls", polls.Load())
	}
}

func TestWaitingForAnExitCodeGivesUp(t *testing.T) {
	c, _ := fakeEngine(t, 1<<30, 0)
	old := exitWait
	exitWait = 300 * time.Millisecond
	defer func() { exitWait = old }()
	if _, err := c.waitExit(context.Background(), "e1"); err == nil {
		t.Error("no error although the command never finished")
	}
}
