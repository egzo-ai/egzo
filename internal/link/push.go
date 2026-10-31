// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/egzo-ai/egzo/internal/hublink"
)

// settleDelay gathers a burst of engine events (a spawn is several) into one update of the project feed.
const settleDelay = 200 * time.Millisecond

// refreshEvery is how often the project is read without an event: `up` publishing templates touches no container, so no event says so.
var refreshEvery = time.Minute

// pushFeeds pushes the three feeds until ctx ends. A feed that ends is opened again, and a feed's engine stream that breaks is
// followed again, with a growing delay: a control sidecar being recreated is an ordinary moment.
func (l *Link) pushFeeds(ctx context.Context, session *yamux.Session) {
	var wg sync.WaitGroup
	for feed, push := range map[string]pusher{hublink.FeedProject: l.pushProject, hublink.FeedStatus: l.pushStatus, hublink.FeedAudit: l.pushAudit} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keepFeed(ctx, session, feed, push)
		}()
	}
	wg.Wait()
}

type pusher func(context.Context, *hublink.Lines, hublink.FeedStart) error

// keepFeed keeps one feed open until ctx ends.
func keepFeed(ctx context.Context, session *yamux.Session, feed string, push pusher) {
	_ = retry(ctx, func() error { return nil }, func() error {
		if err := openFeed(ctx, session, feed, push); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "feed %s: %v\n", feed, err)
		}
		return nil
	})
}

// openFeed is one stream of a feed, from its opening to its end.
func openFeed(ctx context.Context, session *yamux.Session, feed string, push pusher) error {
	stream, err := session.OpenStream()
	if err != nil {
		return err
	}
	defer stream.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		stream.SetDeadline(time.Now())
	}()
	lines := hublink.NewLines(stream)
	if err := lines.Write(hublink.FeedOpen{Feed: feed}); err != nil {
		return err
	}
	var start hublink.FeedStart
	if err := lines.Read(&start); err != nil {
		return err
	}
	ended := make(chan struct{})
	go func() { // the hub says nothing on a feed after its start: the only thing to read is its end, which no write notices
		defer close(ended)
		_, _ = io.Copy(io.Discard, lines.Rest())
		cancel()
	}()
	err = push(ctx, lines, start)
	select {
	case <-ended:
		return errors.New("the hub closed it")
	default:
		return err
	}
}

// retry runs follow again each time it ends, until ctx ends or sending fails.
func retry(ctx context.Context, sendFailed func() error, follow func() error) error {
	delay := firstDelay
	for {
		began := time.Now()
		_ = follow()
		if err := sendFailed(); err != nil {
			return err
		}
		if time.Since(began) > lastDelay {
			delay = firstDelay
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay + rand.N(delay/4)):
		}
		delay = min(delay*2, lastDelay)
	}
}

// sticky remembers the first failure to send, and ends the context what is followed runs in: nothing more can reach the hub.
type sticky struct {
	mu     sync.Mutex
	err    error
	cancel context.CancelFunc
}

// sending is a context that a failure to send ends, and what notes the failure.
func sending(ctx context.Context) (context.Context, *sticky) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, &sticky{cancel: cancel}
}

func (s *sticky) note(err error) {
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err == nil {
			s.err = err
		}
		s.cancel()
	}
}

func (s *sticky) failed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (l *Link) pushProject(ctx context.Context, lines *hublink.Lines, _ hublink.FeedStart) error {
	engine, name := l.options.Engine, l.options.Project
	ctx, send := sending(ctx)
	var feed projectFeed
	push := func() {
		project, err := engine.Project(ctx, name)
		if err != nil {
			return
		}
		published, err := engine.Published(ctx, name)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "read the templates: %v\n", err)
		}
		if update, changed := feed.next(project, published); changed {
			send.note(lines.Write(update))
		}
	}
	wake := make(chan struct{}, 1)
	go func() {
		refresh := time.NewTicker(refreshEvery)
		defer refresh.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
			case <-wake:
				select {
				case <-ctx.Done():
					return
				case <-time.After(settleDelay):
				}
			}
			push()
		}
	}()
	return retry(ctx, send.failed, func() error {
		return engine.WatchProject(ctx, name, func() {
			select {
			case wake <- struct{}{}:
			default:
			}
		})
	})
}

// projectFeed says what the project feed sends next.
type projectFeed struct {
	sent []byte
}

// next takes a reading of the project and returns the update, and whether it says anything new.
func (f *projectFeed) next(project Project, published Published) (hublink.ProjectUpdate, bool) {
	update := hublink.ProjectUpdate{Project: project, Published: published}
	data, err := json.Marshal(update)
	if err != nil || bytes.Equal(data, f.sent) {
		return update, false
	}
	f.sent = data
	return update, true
}

func (l *Link) pushStatus(ctx context.Context, lines *hublink.Lines, _ hublink.FeedStart) error {
	ctx, send := sending(ctx)
	return retry(ctx, send.failed, func() error {
		return l.options.Engine.FollowStatus(ctx, l.options.Project, func(line StatusLine) { send.note(lines.Write(line)) })
	})
}

func (l *Link) pushAudit(ctx context.Context, lines *hublink.Lines, start hublink.FeedStart) error {
	ctx, send := sending(ctx)
	since := start.Since
	deliver := sendAudit(lines, send, &since)
	return retry(ctx, send.failed, func() error {
		return l.options.Engine.FollowAudit(ctx, l.options.Project, since, deliver)
	})
}

// sendAudit sends an event and moves since past it: only past one that went out, or following again would skip it.
func sendAudit(lines *hublink.Lines, send *sticky, since *time.Time) func(AuditEvent) {
	return func(event AuditEvent) {
		if err := lines.Write(event); err != nil {
			send.note(err)
			return
		}
		*since = event.Time
	}
}
