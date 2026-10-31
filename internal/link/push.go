// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/egzo-ai/egzo/internal/hublink"
)

// settleDelay gathers a burst of engine events (a spawn is several) into one update of the project feed.
const settleDelay = 200 * time.Millisecond

// pushFeeds pushes the three feeds until ctx ends. A feed's engine stream that breaks is followed again with a growing delay: a
// control sidecar being recreated is an ordinary moment.
func (l *Link) pushFeeds(ctx context.Context, session *yamux.Session) {
	var wg sync.WaitGroup
	for feed, push := range map[string]func(context.Context, *hublink.Lines, hublink.FeedStart) error{
		hublink.FeedProject: l.pushProject, hublink.FeedStatus: l.pushStatus, hublink.FeedAudit: l.pushAudit,
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := session.OpenStream()
			if err != nil {
				return
			}
			defer stream.Close()
			finished := make(chan struct{})
			defer close(finished)
			go func() {
				select {
				case <-ctx.Done():
					stream.SetDeadline(time.Now())
				case <-finished:
				}
			}()
			lines := hublink.NewLines(stream)
			if err := lines.Write(hublink.FeedOpen{Feed: feed}); err != nil {
				return
			}
			var start hublink.FeedStart
			if err := lines.Read(&start); err != nil {
				return
			}
			if err := push(ctx, lines, start); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "feed %s: %v\n", feed, err)
			}
		}()
	}
	wg.Wait()
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

// sticky remembers the first failure to send.
type sticky struct {
	mu  sync.Mutex
	err error
}

func (s *sticky) note(err error) {
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err == nil {
			s.err = err
		}
	}
}

func (s *sticky) failed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (l *Link) pushProject(ctx context.Context, lines *hublink.Lines, _ hublink.FeedStart) error {
	engine, project := l.options.Engine, l.options.Project
	var send sticky
	push := func() {
		update := hublink.ProjectUpdate{}
		var err error
		if update.Project, err = engine.Project(ctx, project); err != nil {
			return
		}
		if update.Published, err = engine.Published(ctx, project); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "read the templates: %v\n", err)
		}
		send.note(lines.Write(update))
	}
	push()
	var pending *time.Timer
	var mu sync.Mutex
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if pending != nil {
			pending.Stop()
		}
	}()
	return retry(ctx, send.failed, func() error {
		return engine.WatchProject(ctx, project, func() {
			mu.Lock()
			defer mu.Unlock()
			if pending == nil {
				pending = time.AfterFunc(settleDelay, func() {
					mu.Lock()
					pending = nil
					mu.Unlock()
					push()
				})
			}
		})
	})
}

func (l *Link) pushStatus(ctx context.Context, lines *hublink.Lines, _ hublink.FeedStart) error {
	var send sticky
	return retry(ctx, send.failed, func() error {
		return l.options.Engine.FollowStatus(ctx, l.options.Project, func(line StatusLine) { send.note(lines.Write(line)) })
	})
}

func (l *Link) pushAudit(ctx context.Context, lines *hublink.Lines, start hublink.FeedStart) error {
	var send sticky
	since := start.Since
	return retry(ctx, send.failed, func() error {
		return l.options.Engine.FollowAudit(ctx, l.options.Project, since, func(event AuditEvent) {
			since = event.Time
			send.note(lines.Write(event))
		})
	})
}
