// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sendAs(t *testing.T, r *rig, from, to, text string) string {
	t.Helper()
	id, err := r.srv.sendFromAgent(from, "agent:"+to, text, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWaitReturnsWhenWhatWasSentIsFetchedUpdatedOrResolved(t *testing.T) {
	r := newRig(t)
	r.project("lead", "helper")
	id := sendAs(t, r, "lead", "helper", "count")
	steps := []func(){
		func() { r.srv.fetch("helper", id) },
		func() { r.srv.update("helper", id, "half way") },
		func() { r.srv.resolve("helper", id, "230", "done") },
	}
	wantState := []string{stateFetched, stateFetched, stateResolved}
	for i, step := range steps {
		go func() { time.Sleep(50 * time.Millisecond); step() }()
		got, err := r.srv.wait(context.Background(), "lead", []string{id}, 5*time.Second)
		if err != nil || !got.Changed || got.Sent[0].State != wantState[i] {
			t.Fatalf("step %d: %+v %v", i, got, err)
		}
	}
	last, _ := r.srv.wait(context.Background(), "lead", []string{id}, time.Second)
	if last.Sent[0].Reply == "" || last.Sent[0].LatestUpdate == nil || last.Sent[0].LatestUpdate.Text != "half way" {
		t.Errorf("a resolved request names its reply and keeps its latest update: %+v", last.Sent[0])
	}
}

func TestWaitEndsAtItsDeadlineWhenAMessageArrivesAndOneAtATime(t *testing.T) {
	r := newRig(t)
	r.project("lead", "helper", "other")
	id := sendAs(t, r, "lead", "helper", "count")
	start := time.Now()
	got, err := r.srv.wait(context.Background(), "lead", []string{id}, time.Second)
	if err != nil || got.Changed || time.Since(start) < time.Second {
		t.Errorf("nothing happened: %+v %v after %v", got, err, time.Since(start))
	}
	go func() { time.Sleep(50 * time.Millisecond); sendAs(t, r, "other", "lead", "a question for you") }()
	got, _ = r.srv.wait(context.Background(), "lead", []string{id}, 5*time.Second)
	if !got.Changed || len(got.Arrived) != 1 {
		t.Errorf("a message for the waiting agent ends the wait: %+v", got)
	}
	done := make(chan struct{})
	go func() { r.srv.wait(context.Background(), "lead", []string{id}, 2*time.Second); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if _, err := r.srv.wait(context.Background(), "lead", []string{id}, time.Second); !errors.Is(err, errWaitRunning) {
		t.Errorf("a second wait at once: %v", err)
	}
	<-done
	if _, err := r.srv.wait(context.Background(), "other", []string{id}, time.Second); !errors.Is(err, errNoSuchMessage) {
		t.Errorf("waiting on someone else's message: %v", err)
	}
	if _, err := r.srv.wait(context.Background(), "lead", nil, time.Second); err == nil {
		t.Error("a wait on nothing was taken")
	}
}
