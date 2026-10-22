package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/engines/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// quick is a feed setting where nothing waits for long.
var quick = FeedOptions{Fallback: time.Hour, Fast: 20 * time.Millisecond, BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	Silent: 150 * time.Millisecond, Debounce: 5 * time.Millisecond, Healthy: time.Hour, Grace: -1}

func (r *rig) startFeeds(opt FeedOptions) {
	r.t.Helper()
	feedCtx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	r.hub.StartFeeds(feedCtx, opt)
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func (r *rig) agentOf(user store.User, engineName, project, agent string) (engines.Agent, Feed) {
	r.t.Helper()
	refs, _, err := r.hub.Projects(ctx, user)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, ref := range refs {
		if ref.Engine == engineName && ref.Project.Name == project {
			for _, a := range ref.Project.Agents {
				if a.Name == agent {
					return a, ref.Feed
				}
			}
		}
	}
	return engines.Agent{}, Feed{}
}

func running(name string, activity string) engines.Agent {
	return engines.Agent{Name: name, Template: "coder", State: "running", Activity: activity, Actor: "operator", Created: time.Now()}
}

func TestTheStatusOfAnAgentReachesTheProjectListThroughTheFeedAndFollowsChanges(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := running("coder-1", "working")
	a.Status, a.Waiting = "refactoring", true
	r.fakes["local"].SetAgent("web", a)
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the feed to be live and carry the agent's state", func() bool {
		got, feed := r.agentOf(op, "local", "web", "coder-1")
		return feed.State == FeedLive && got.Activity == "working" && got.Waiting && got.Status == "refactoring"
	})
	// a change in the sidecar's log reaches the list with no scan and no poll: the list is what the feed last said
	r.fakes["local"].SetAgent("web", running("coder-1", "idle"))
	eventually(t, "the change to arrive", func() bool {
		got, _ := r.agentOf(op, "local", "web", "coder-1")
		return got.Activity == "idle" && !got.Waiting
	})
}

func TestAnAgentWhoseStateTheFeedCannotSayIsUnknownAndNeverStarting(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", running("coder-1", "working"))
	r.fakes["local"].SetFeedSilent("web", true) // a sidecar that logs no status: an old one, or a log that cannot be read
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	got, feed := r.agentOf(op, "local", "web", "coder-1")
	if got.Activity != ActivityUnknown || feed.State != FeedConnecting {
		t.Errorf("before the silence is a verdict: %q, %+v", got.Activity, feed)
	}
	eventually(t, "the feed to be called unavailable", func() bool {
		_, feed := r.agentOf(op, "local", "web", "coder-1")
		return feed.State == FeedUnavailable && strings.Contains(feed.Message, "writes no status")
	})
	if got, _ := r.agentOf(op, "local", "web", "coder-1"); got.Activity != ActivityUnknown {
		t.Errorf("activity = %q", got.Activity)
	}
}

func TestAStoppedAgentIsStoppedWhatEverTheFeedSays(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	a := running("coder-1", "working")
	a.State = "exited"
	a.Waiting = true
	r.fakes["local"].SetAgent("web", a)
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the feed", func() bool { _, feed := r.agentOf(op, "local", "web", "coder-1"); return feed.State == FeedLive })
	if got, _ := r.agentOf(op, "local", "web", "coder-1"); got.Activity != "stopped" || got.Waiting {
		t.Errorf("a stopped agent is %+v", got)
	}
}

func TestABrokenStreamIsRetriedWithADelayAndMendsItself(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", running("coder-1", "idle"))
	r.fakes["local"].SetFeedError("web", errors.New("configured logging driver does not support reading"))
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the failure to be reported with its reason", func() bool {
		_, feed := r.agentOf(op, "local", "web", "coder-1")
		return feed.State == FeedUnavailable && strings.Contains(feed.Message, "does not support reading")
	})
	// it keeps trying, with a delay between tries (5 to 20 milliseconds here), and not in a loop
	eventually(t, "several tries", func() bool { return r.fakes["local"].FeedTries("web") >= 4 })
	if n := r.fakes["local"].FeedTries("web"); n > 400 {
		t.Errorf("%d tries: the delay does not hold it back", n)
	}
	r.fakes["local"].SetFeedError("web", nil)
	eventually(t, "the stream to come back", func() bool {
		got, feed := r.agentOf(op, "local", "web", "coder-1")
		return feed.State == FeedLive && got.Activity == "idle"
	})
}

func TestTheDelayBeforeAnotherTryGrowsToALimitAndStartsOver(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 800*time.Millisecond)
	for i, want := range []time.Duration{100, 200, 400, 800, 800, 800} {
		want *= time.Millisecond
		got := b.wait()
		if got < want*8/10 || got > want*12/10 {
			t.Errorf("wait %d = %v, want about %v", i, got, want)
		}
	}
	b.reset()
	if got := b.wait(); got > 120*time.Millisecond {
		t.Errorf("after a reset = %v", got)
	}
}

func TestFollowersStopWhenTheirSidecarOrTheirEngineIsGone(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].AddProject(project("api", "coder"))
	r.startFeeds(quick)
	r.scan()
	eventually(t, "both projects to be followed", func() bool { return r.fakes["local"].Feeds("web") == 1 && r.fakes["local"].Feeds("api") == 1 })

	r.fakes["local"].RemoveProject("api")
	r.scan()
	eventually(t, "the follower of a removed project to stop", func() bool { return r.fakes["local"].Feeds("api") == 0 })

	// a sidecar that is not running is not followed
	p := project("web", "coder")
	p.Services = []engines.Service{{Name: "control", State: "exited", ID: "c1"}, {Name: "proxy", State: "running"}}
	r.fakes["local"].AddProject(p)
	r.scan()
	eventually(t, "the follower of a stopped sidecar to stop", func() bool { return r.fakes["local"].Feeds("web") == 0 })

	// and an engine that does not answer is no place to open streams
	r.fakes["local"].AddProject(project("web", "coder"))
	r.scan()
	eventually(t, "the follower to come back", func() bool { return r.fakes["local"].Feeds("web") == 1 })
	r.fakes["local"].SetDown(fake.ErrUnreachable)
	r.scan()
	eventually(t, "followers to stop on an engine that is down", func() bool { return r.fakes["local"].Feeds("web") == 0 })
}

func TestASidecarThatWasMadeAgainIsFollowedAgain(t *testing.T) {
	r := newRig(t, "local")
	p := project("web", "coder")
	p.Services = []engines.Service{{Name: "control", State: "running", ID: "one"}, {Name: "proxy", State: "running"}}
	r.fakes["local"].AddProject(p)
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the first follower", func() bool { return r.fakes["local"].FeedStarts("web") == 1 })
	r.scan()
	time.Sleep(30 * time.Millisecond)
	if n := r.fakes["local"].FeedStarts("web"); n != 1 {
		t.Errorf("an unchanged sidecar was followed %d times", n)
	}
	p.Services = []engines.Service{{Name: "control", State: "running", ID: "two"}, {Name: "proxy", State: "running"}}
	r.fakes["local"].AddProject(p)
	r.scan()
	eventually(t, "a follower for the new sidecar", func() bool { return r.fakes["local"].FeedStarts("web") == 2 })
	if n := r.fakes["local"].Feeds("web"); n != 1 {
		t.Errorf("followers = %d: the old one must stop", n)
	}
}

func TestAQuietSidecarIsStaleAfterThreeHeartbeats(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", running("coder-1", "idle"))
	r.fakes["local"].SetFeedInterval(time.Hour) // one line, then nothing for a long time
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the first line", func() bool { _, feed := r.agentOf(op, "local", "web", "coder-1"); return feed.State == FeedLive })
	// pretend the line is old: the follower heard it long ago, and three heartbeats of an hour have not passed, so it is still good
	m := r.hub.feedManager()
	m.mu.Lock()
	f := m.followers[projectKey{"local", "web"}]
	f.lastLine = time.Now().Add(-2 * time.Hour)
	f.interval = 10 * time.Millisecond
	m.mu.Unlock()
	got, feed := r.agentOf(op, "local", "web", "coder-1")
	if feed.State != FeedStale || !strings.Contains(feed.Message, "no status") || got.Activity != ActivityUnknown {
		t.Errorf("a silent sidecar: %+v activity %q", feed, got.Activity)
	}
}

func TestALineReplayedWhenTheStreamOpensIsAsOldAsItWas(t *testing.T) {
	r := newRig(t, "local")
	r.startFeeds(quick)
	m := r.hub.feedManager()
	f := &follower{since: time.Now(), agents: map[string]engines.AgentStatus{}}
	old := engines.StatusLine{Time: time.Now().Add(-time.Hour), Interval: 30 * time.Second, Agents: []engines.AgentStatus{{Agent: "coder-1", Activity: "idle"}}}
	m.apply(f, old, true)
	if feed := m.state(f, time.Now()); feed.State != FeedStale {
		t.Errorf("a replayed hour-old line makes the sidecar look %q", feed.State)
	}
	m.apply(f, engines.StatusLine{Time: time.Now(), Interval: 30 * time.Second, Agents: old.Agents}, false)
	if feed := m.state(f, time.Now()); feed.State != FeedLive {
		t.Errorf("a line that arrives live: %q", feed.State)
	}
}

func TestAPageThatPollsAsksTheEngineForNothingAndTheTemplatesAreKept(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder", "reviewer"))
	a := running("coder-1", "idle")
	a.TemplateHash = "old"
	r.fakes["local"].SetAgent("web", a)
	b := running("reviewer-1", "idle")
	b.Template, b.TemplateHash = "reviewer", "hash-reviewer"
	r.fakes["local"].SetAgent("web", b)
	op := r.operator()
	r.scan()
	for i := 0; i < 20; i++ {
		view, err := r.hub.Project(ctx, op, "local", "web")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(view.Project.Templates, ",") != "coder,reviewer" {
			t.Fatalf("templates = %v", view.Project.Templates)
		}
		for _, v := range view.Agents {
			if want := v.Name == "coder-1"; v.Stale != want {
				t.Fatalf("%s stale = %v: its template changed since it was made: %v", v.Name, v.Stale, want)
			}
		}
	}
	if n := r.fakes["local"].ProjectReads(); n != 0 {
		t.Errorf("the page read the project from the engine %d times", n)
	}
	if n := r.fakes["local"].PublishedReads(); n != 1 {
		t.Errorf("the templates were asked of the sidecar %d times for 20 views, want 1", n)
	}
}

func TestTheTemplatesAreAskedAgainWhenTheSidecarIsAnotherContainer(t *testing.T) {
	r := newRig(t, "local")
	p := project("web", "coder")
	p.Services = []engines.Service{{Name: "control", State: "running", ID: "one"}, {Name: "proxy", State: "running"}}
	r.fakes["local"].AddProject(p)
	op := r.operator()
	r.scan()
	r.hub.Project(ctx, op, "local", "web")
	p.Services = []engines.Service{{Name: "control", State: "running", ID: "two"}, {Name: "proxy", State: "running"}}
	r.fakes["local"].AddProject(p)
	r.scan()
	r.hub.Project(ctx, op, "local", "web")
	if n := r.fakes["local"].PublishedReads(); n != 2 {
		t.Errorf("reads = %d", n)
	}
}

func TestAFailureToReadTheTemplatesIsKeptSoThePageDoesNotAskOnEveryPoll(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetPublished("web", engines.Published{})
	op := r.operator()
	r.scan()
	r.fakes["local"].SetDown(errors.New("control sidecar: 404 page not found"))
	for i := 0; i < 10; i++ {
		view, err := r.hub.Project(ctx, op, "local", "web")
		if err != nil {
			// the engine is down: the page may fail, but the sidecar is asked at most once for it
			continue
		}
		_ = view
	}
	if n := r.fakes["local"].PublishedReads(); n > 1 {
		t.Errorf("the sidecar was asked %d times", n)
	}
}

func TestEventsMakeTheHubListAnEngineAgainWithNoTimerInvolved(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.hub.Run(runCtx, quick); close(done) }() // the fallback is an hour: only events can explain what follows
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the first listing", func() bool { refs, _, _ := r.hub.Projects(ctx, op); return len(refs) == 1 })
	r.fakes["local"].AddProject(project("new", "coder"))
	eventually(t, "the new project to be listed because an event said so", func() bool {
		refs, _, _ := r.hub.Projects(ctx, op)
		return len(refs) == 2
	})
	r.fakes["local"].RemoveProject("web")
	eventually(t, "the removal to be listed", func() bool { refs, _, _ := r.hub.Projects(ctx, op); return len(refs) == 1 })
}

func TestWithoutEventsAnEngineIsListedOnTheShortTimerAndTheWatchIsRetried(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].SetWatchError(errors.New("events are not supported"))
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.hub.Run(runCtx, quick); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the first listing", func() bool { refs, _, _ := r.hub.Projects(ctx, op); return len(refs) == 1 })
	r.fakes["local"].AddProject(project("new", "coder"))
	eventually(t, "the project to be found by the short timer", func() bool { refs, _, _ := r.hub.Projects(ctx, op); return len(refs) == 2 })
	eventually(t, "the watch to be tried again and again", func() bool { return r.fakes["local"].WatchTries() >= 3 })
	if r.fakes["local"].WatchStarts() != 0 {
		t.Error("a watch that fails was counted as started")
	}
	r.fakes["local"].SetWatchError(nil)
	eventually(t, "the watch to start once the engine can", func() bool { return r.fakes["local"].WatchStarts() >= 1 })
}

func TestAnOperatorSeesTheFeedOfEveryProjectAndAUserOnlyTheirs(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	r.fakes["local"].SetAgent("web", running("coder-1", "working"))
	r.operator()
	alice := r.login("alice")
	r.startFeeds(quick)
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	eventually(t, "the feed", func() bool {
		got, feed := r.agentOf(alice, "local", "web", "coder-1")
		return feed.State == FeedLive && got.Activity == "working"
	})
}

func TestRemovingAnEngineStopsItsStreamsAtOnce(t *testing.T) {
	r := newRig(t, "local", "lab")
	r.fakes["lab"].AddProject(project("data", "coder"))
	op := r.operator()
	r.startFeeds(quick)
	r.scan()
	eventually(t, "the engine to be followed and watched", func() bool { return r.fakes["lab"].Feeds("data") == 1 && r.fakes["lab"].WatchStarts() >= 1 })
	if err := r.hub.RemoveEngine(ctx, op, "lab"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follower of the removed engine to stop", func() bool { return r.fakes["lab"].Feeds("data") == 0 })
	m := r.hub.feedManager()
	m.mu.Lock()
	_, watched := m.watches["lab"]
	m.mu.Unlock()
	if watched {
		t.Error("the event watcher of a removed engine is still there")
	}
}
