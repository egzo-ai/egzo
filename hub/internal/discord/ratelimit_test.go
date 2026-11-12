// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
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

func TestAMessageWithANonceIsPostedAgainAfterAServerErrorOrALostConnection(t *testing.T) {
	retryBase = 10 * time.Millisecond
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusGatewayTimeout)
		case 2: // the connection drops before an answer
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		default:
			w.Write([]byte(`{"id": "9"}`))
		}
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	m, err := c.CreateMessage(context.Background(), "20", MessageCreate{Content: "hi", Nonce: "n1", EnforceNonce: true})
	if err != nil || m.ID != "9" || calls.Load() != 3 {
		t.Errorf("posted %+v, %v, after %d calls", m, err, calls.Load())
	}
}

func TestOnlyARefusalThatCannotPassLaterIsPermanent(t *testing.T) {
	for err, want := range map[error]bool{
		&Error{Status: 400}:      true,
		&Error{Status: 403}:      true,
		&Error{Status: 404}:      true,
		&Error{Status: 429}:      false,
		&Error{Status: 500}:      false,
		ErrTooManyInvalid:        false,
		errors.New("connection"): false,
	} {
		if Permanent(err) != want {
			t.Errorf("%v: permanent = %v", err, !want)
		}
	}
}

// limited is a route as Discord limits it: so many requests per window, the rest answered 429.
type limited struct {
	limit  int
	window time.Duration
	// backwards makes the answers of requests sent together come back in the reverse of the order they were counted in.
	backwards bool

	mu      sync.Mutex
	began   time.Time
	used    int
	refused int
}

func (l *limited) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	time.Sleep(5 * time.Millisecond) // the time an answer takes: requests sent together are here together
	l.mu.Lock()
	if time.Since(l.began) >= l.window {
		l.began, l.used = time.Now(), 0
	}
	left := time.Until(l.began.Add(l.window))
	refused := l.used >= l.limit
	if refused {
		l.refused++
	} else {
		l.used++
	}
	remaining := l.limit - l.used
	l.mu.Unlock()
	if l.backwards {
		time.Sleep(time.Duration(remaining) * 10 * time.Millisecond)
		left -= time.Duration(remaining) * 10 * time.Millisecond
	}
	w.Header().Set("X-RateLimit-Bucket", "abcd")
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset-After", strconv.FormatFloat(left.Seconds(), 'f', 3, 64))
	if refused {
		w.Header().Set("X-RateLimit-Scope", "user")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"message": "You are being rate limited.", "retry_after": %.3f, "global": false}`, left.Seconds())
		return
	}
	w.Write([]byte(`{}`))
}

func TestRequestsSentTogetherDoNotBurstWhileTheBucketIsNotKnown(t *testing.T) {
	route := &limited{limit: 5, window: 300 * time.Millisecond}
	srv := httptest.NewServer(route)
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 12 { // the first window (no answer yet), and twice a window that just reset
		wg.Go(func() {
			if err := c.Do(context.Background(), http.MethodPost, "/channels/20/messages", map[string]string{}, nil); err != nil {
				failed.Add(1)
				t.Log(err)
			}
		})
	}
	wg.Wait()
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.refused != 0 || failed.Load() != 0 {
		t.Errorf("Discord refused %d requests with 429, and %d of 12 were lost", route.refused, failed.Load())
	}
}

func TestOnceTheLimitIsKnownOnlyWhatIsLeftGoesBeforeTheReset(t *testing.T) {
	route := &limited{limit: 5, window: 300 * time.Millisecond, backwards: true}
	srv := httptest.NewServer(route)
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	ctx := context.Background()
	if err := c.Do(ctx, http.MethodGet, "/channels/20", nil, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 { // what is left, together: their answers come back in any order
		wg.Go(func() { _ = c.Do(ctx, http.MethodGet, "/channels/20", nil, nil) })
	}
	wg.Wait()
	if err := c.Do(ctx, http.MethodGet, "/channels/20", nil, nil); err != nil {
		t.Fatal(err)
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.refused != 0 {
		t.Errorf("the sixth request did not wait for the reset: Discord refused %d", route.refused)
	}
}

func TestARouteDiscordDoesNotLimitIsNotHeldBack(t *testing.T) {
	var now, most atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := now.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(50 * time.Millisecond)
		now.Add(-1)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	ctx := context.Background()
	if err := c.Do(ctx, http.MethodGet, "/gateway/bot", nil, nil); err != nil { // the first answer says there is no limit
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { _ = c.Do(ctx, http.MethodGet, "/gateway/bot", nil, nil) })
	}
	wg.Wait()
	if most.Load() < 2 {
		t.Error("6 requests sent together went one at a time")
	}
}

func TestAnswersToInteractionsDoNotWaitOnEachOther(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Bucket", "abcd") // each token has a limit of its own: this one is spent, the next person's is not
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "5")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewREST("t", srv.URL, nil)
	ctx := context.Background()
	began := time.Now()
	for n := range 3 {
		token := "tok-" + strconv.Itoa(n)
		if err := c.Do(ctx, http.MethodPost, "/interactions/"+strconv.Itoa(55+n)+"/"+token+"/callback", map[string]int{"type": 5}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Do(ctx, http.MethodPatch, "/webhooks/2/"+token+"/messages/@original", map[string]string{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("one person's answer waited on another's limit: %v", took)
	}
	for path, want := range map[string]bool{
		"/interactions/55/tok/callback":      true,
		"/webhooks/2/tok":                    true,
		"/webhooks/2/tok/messages/@original": true,
		"/webhooks/2":                        false,
		"/channels/20/webhooks":              false,
		"/channels/20/messages":              false,
	} {
		if withToken(path) != want {
			t.Errorf("%s: with a token = %v", path, !want)
		}
	}
}
