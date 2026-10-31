// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/hublink"
	"github.com/egzo-ai/egzo/internal/stack"
)

func TestAFeedTheHubClosedIsOpenedAgain(t *testing.T) {
	hub, session := sessions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushed := make(chan struct{}, 4)
	go keepFeed(ctx, session, hublink.FeedAudit, func(ctx context.Context, _ *hublink.Lines, _ hublink.FeedStart) error {
		pushed <- struct{}{}
		<-ctx.Done() // a follower that writes nothing: only the end of the stream tells it to stop
		return nil
	})
	for range 2 {
		accepted := make(chan error, 1)
		go func() {
			stream, err := hub.AcceptStream()
			if err == nil {
				lines := hublink.NewLines(stream)
				var open hublink.FeedOpen
				if err = lines.Read(&open); err == nil {
					err = lines.Write(hublink.FeedStart{})
				}
				defer stream.Close()
			}
			accepted <- err
		}()
		select {
		case err := <-accepted:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the feed was not opened again")
		}
		select {
		case <-pushed:
		case <-time.After(5 * time.Second):
			t.Fatal("nothing was pushed")
		}
	}
}

func TestAFailureToSendStopsWhatIsFollowed(t *testing.T) {
	ctx, send := sending(context.Background())
	failure := errors.New("the stream is gone")
	send.note(nil)
	if ctx.Err() != nil || send.failed() != nil {
		t.Fatal("no failure yet")
	}
	send.note(failure)
	if ctx.Err() == nil || !errors.Is(send.failed(), failure) {
		t.Errorf("ctx %v, failed %v", ctx.Err(), send.failed())
	}
}

func TestAnAuditEventThatDidNotGoOutIsFollowedAgain(t *testing.T) {
	_, send := sending(context.Background())
	began := time.Unix(1000, 0)
	since := began
	sendAudit(hublink.NewLines(broken{}), send, &since)(AuditEvent{Time: began.Add(time.Hour)})
	if !since.Equal(began) || send.failed() == nil {
		t.Errorf("since %v, failed %v", since, send.failed())
	}
	stream := heard{said: make(chan string, 1)}
	sendAudit(hublink.NewLines(stream), send, &since)(AuditEvent{Time: began.Add(time.Hour)})
	if !since.Equal(began.Add(time.Hour)) {
		t.Errorf("since %v", since)
	}
}

func TestTemplatesThatCannotBeReadAreTheOnesLastRead(t *testing.T) {
	var feed projectFeed
	failure := errors.New("the control sidecar is being recreated")
	if _, changed := feed.next(Project{Name: "web"}, Published{}, failure); changed {
		t.Error("an update without templates would make every agent stale")
	}
	templates := Published{Templates: map[string]string{"coder": "abc"}}
	if update, changed := feed.next(Project{Name: "web"}, templates, nil); !changed || update.Published.Templates["coder"] != "abc" {
		t.Fatalf("got %+v, %v", update, changed)
	}
	update, changed := feed.next(Project{Name: "web", Agents: []Agent{{Name: "a"}}}, Published{}, failure)
	if !changed || update.Published.Templates["coder"] != "abc" {
		t.Errorf("got %+v, %v", update, changed)
	}
}

func TestAProjectWithoutTemplatesYetSaysSo(t *testing.T) {
	var feed projectFeed
	if update, changed := feed.next(Project{Name: "web"}, Published{}, stack.ErrNotPublished); !changed || len(update.Published.Templates) != 0 {
		t.Errorf("got %+v, %v", update, changed)
	}
}

func TestAnUpdateThatSaysNothingNewIsNotSent(t *testing.T) {
	var feed projectFeed
	templates := Published{Templates: map[string]string{"coder": "abc"}}
	if _, changed := feed.next(Project{Name: "web"}, templates, nil); !changed {
		t.Fatal("the first update says something")
	}
	if _, changed := feed.next(Project{Name: "web"}, templates, nil); changed {
		t.Error("the same update again")
	}
	if _, changed := feed.next(Project{Name: "web"}, Published{Templates: map[string]string{"coder": "def"}}, nil); !changed {
		t.Error("templates published without touching a container went unsaid")
	}
}
