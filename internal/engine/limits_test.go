// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
)

// junkEngine is a Docker API that answers every request with size bytes of a JSON array, or of one event without its newline.
func junkEngine(t *testing.T, size int) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/events"):
			fmt.Fprint(w, `{"Type":"container","Action":"start","Actor":{"ID":"`+strings.Repeat("a", size)+`"}}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write(make([]byte, size))
		default:
			fmt.Fprint(w, "["+strings.Repeat(" ", size-2)+"]")
		}
	}))
	t.Cleanup(server.Close)
	c, err := Dial(context.Background(), Target{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAnAnswerOfAnEngineIsReadUpToMaxAnswer(t *testing.T) {
	if _, err := junkEngine(t, MaxAnswer).API.ContainerList(context.Background(), container.ListOptions{}); err != nil {
		t.Errorf("an answer of exactly MaxAnswer: %v", err)
	}
	_, err := junkEngine(t, MaxAnswer+1).API.ContainerList(context.Background(), container.ListOptions{})
	var tooLarge ErrAnswerTooLarge
	if !errors.As(err, &tooLarge) || !strings.Contains(err.Error(), "/containers/json") {
		t.Errorf("an answer over MaxAnswer: %v", err)
	}
}

func TestAStreamIsNotHeldToMaxAnswer(t *testing.T) {
	reader, err := junkEngine(t, MaxAnswer+1<<20).API.ContainerLogs(context.Background(), "x", container.LogsOptions{ShowStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if n, err := io.Copy(io.Discard, reader); err != nil || n != MaxAnswer+1<<20 {
		t.Errorf("read %d bytes of a log, err %v", n, err)
	}
}

func TestAnEventLongerThanMaxEventLineEndsTheEventStream(t *testing.T) {
	_, errs := junkEngine(t, MaxEventLine+1).API.Events(context.Background(), events.ListOptions{})
	var tooLarge ErrAnswerTooLarge
	if err := <-errs; !errors.As(err, &tooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestWhatACommandWritesIsKeptUpToMaxAnswer(t *testing.T) {
	b := boundedBuffer{what: "the output of `x`"}
	if _, err := b.Write(make([]byte, MaxAnswer)); err != nil {
		t.Fatal(err)
	}
	var tooLarge ErrAnswerTooLarge
	if _, err := b.Write([]byte{0}); !errors.As(err, &tooLarge) || b.Len() != MaxAnswer {
		t.Errorf("err = %v, kept %d", err, b.Len())
	}
}
