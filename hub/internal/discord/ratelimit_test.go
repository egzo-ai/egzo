// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestARouteKeepsItsMajorParameterAndNeverATokenOrAMinorID(t *testing.T) {
	for path, want := range map[string]string{
		"/channels/20/messages":                "POST channels/20/messages",
		"/channels/20/messages/99":             "POST channels/20/messages/{id}",
		"/guilds/10/roles/77":                  "POST guilds/10/roles/{id}",
		"/interactions/55/secret-tok/callback": "POST interactions/{id}/{token}/callback",
		"/webhooks/2/secret-tok":               "POST webhooks/2/{token}",
	} {
		if got := route(http.MethodPost, path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	if route(http.MethodDelete, "/channels/20/messages/99") != "DELETE message 20" {
		t.Error("deleting a message has a limit of its own")
	}
}

func TestABucketThatIsSpentWaitsForItsReset(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(2-n)))
		w.Header().Set("X-RateLimit-Reset-After", "0.2")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	began := time.Now()
	for range 3 {
		if err := c.Do(context.Background(), http.MethodGet, "/channels/20", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(began); took < 200*time.Millisecond {
		t.Errorf("the third request did not wait for the reset: %v", took)
	}
}

func TestTooManyInvalidRequestsHoldTheClientBackBeforeDiscordBansIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message": "Missing Access", "code": 50001}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	c.invalidLimit = 20
	for range 20 {
		_ = c.Do(context.Background(), http.MethodGet, "/channels/20", nil, nil)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/channels/20", nil, nil); !errors.Is(err, ErrTooManyInvalid) {
		t.Errorf("err = %v", err)
	}
	c.now = func() time.Time { return time.Now().Add(invalidWindow + time.Second) }
	if err := c.Do(context.Background(), http.MethodGet, "/channels/20", nil, nil); errors.Is(err, ErrTooManyInvalid) {
		t.Error("ten minutes later it was still held back")
	}
}

func TestAGlobal429HoldsEveryRoute(t *testing.T) {
	var limited atomic.Bool
	limited.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limited.Swap(false) {
			w.Header().Set("X-RateLimit-Global", "true")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message": "You are being rate limited.", "retry_after": 0.2, "global": true}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	began := time.Now()
	if err := c.Do(context.Background(), http.MethodGet, "/channels/20", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/guilds/10", nil, nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < 200*time.Millisecond {
		t.Errorf("did not wait out the global limit: %v", took)
	}
}

func TestRoutesThatDiscordSaysShareABucketWaitOnEachOtherButOnlyForTheSameChannel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Bucket", "shared-hash")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "0.3")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	ctx := context.Background()
	if err := c.Do(ctx, http.MethodPost, "/channels/20/messages", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := c.Do(ctx, http.MethodPost, "/channels/30/messages", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 150*time.Millisecond {
		t.Errorf("another channel waited on this one's bucket: %v", took)
	}
	// A route learns its bucket from its own first answer: after that, it and the first route wait on the same one.
	if err := c.Do(ctx, http.MethodPatch, "/channels/20/messages/99", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	began = time.Now()
	if err := c.Do(ctx, http.MethodPost, "/channels/20/messages", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < 250*time.Millisecond {
		t.Errorf("a route in the bucket another spent did not wait for its reset: %v", took)
	}
}

func TestAServerErrorIsTriedAgainButAMessageIsNotPostedTwiceWhenDiscordMayHaveTakenIt(t *testing.T) {
	retryBase = 10 * time.Millisecond
	answers := map[string][]int{}
	var calls = map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		n := calls[key]
		calls[key]++
		if n < len(answers[key]) {
			w.WriteHeader(answers[key][n])
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	answers["GET /channels/20"] = []int{502, 500}
	answers["POST /channels/20/messages"] = []int{503}
	answers["POST /channels/30/messages"] = []int{500}
	c := NewREST("t", srv.URL, nil)
	ctx := context.Background()
	if err := c.Do(ctx, http.MethodGet, "/channels/20", nil, nil); err != nil || calls["GET /channels/20"] != 3 {
		t.Errorf("a read after two server errors: %v, %d calls", err, calls["GET /channels/20"])
	}
	if err := c.Do(ctx, http.MethodPost, "/channels/20/messages", map[string]string{}, nil); err != nil || calls["POST /channels/20/messages"] != 2 {
		t.Errorf("a message after a 503: %v, %d calls", err, calls["POST /channels/20/messages"])
	}
	var e *Error
	if err := c.Do(ctx, http.MethodPost, "/channels/30/messages", map[string]string{}, nil); !errors.As(err, &e) || e.Status != 500 || calls["POST /channels/30/messages"] != 1 {
		t.Errorf("a message after a 500 is not posted again: %v, %d calls", err, calls["POST /channels/30/messages"])
	}
}
