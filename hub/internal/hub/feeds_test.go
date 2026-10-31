package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// quick is a feed setting where nothing waits for long.
var quick = FeedOptions{Silent: 150 * time.Millisecond, Grace: -1}

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

func (r *rig) agentOf(user store.User, project, agent string) (links.Agent, Feed) {
	r.t.Helper()
	refs, err := r.hub.Projects(ctx, user)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, ref := range refs {
		if ref.ID == project {
			for _, a := range ref.Project.Agents {
				if a.Name == agent {
					return a, ref.Feed
				}
			}
		}
	}
	return links.Agent{}, Feed{}
}

func running(name string, activity string) links.Agent {
	return links.Agent{Name: name, Template: "coder", State: "running", Activity: activity, Actor: "operator", Created: time.Now()}
}

func TestTheStatusOfAnAgentReachesTheProjectListThroughTheFeedAndFollowsChanges(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	a := running("coder-1", "working")
	a.Status, a.Waiting = "refactoring", true
	r.fakes["web"].SetAgent(a)
	op := r.operator()
	eventually(t, "the feed to be live and carry the agent's state", func() bool {
		got, feed := r.agentOf(op, r.id("web"), "coder-1")
		return feed.State == FeedLive && got.Activity == "working" && got.Waiting && got.Status == "refactoring"
	})
	// a change in the sidecar's log reaches the list with no scan and no poll: the list is what the feed last said
	r.fakes["web"].SetAgent(running("coder-1", "idle"))
	eventually(t, "the change to arrive", func() bool {
		got, _ := r.agentOf(op, r.id("web"), "coder-1")
		return got.Activity == "idle" && !got.Waiting
	})
}

func TestAnAgentWhoseStateTheFeedCannotSayIsUnknownAndNeverStarting(t *testing.T) {
	r := newRig(t)
	f := fake.New("web", "laptop", "coder")
	f.SetAgent(running("coder-1", "working"))
	f.SetFeedSilent(true) // a sidecar that logs no status: an old one, or a log that cannot be read
	op := r.operator()
	r.adopt(f)
	got, feed := r.agentOf(op, r.id("web"), "coder-1")
	if got.Activity != ActivityUnknown || feed.State != FeedConnecting {
		t.Errorf("before the silence is a verdict: %q, %+v", got.Activity, feed)
	}
	eventually(t, "the feed to be called unavailable", func() bool {
		_, feed := r.agentOf(op, r.id("web"), "coder-1")
		return feed.State == FeedUnavailable && strings.Contains(feed.Message, "writes no status")
	})
	if got, _ := r.agentOf(op, r.id("web"), "coder-1"); got.Activity != ActivityUnknown {
		t.Errorf("activity = %q", got.Activity)
	}
}

func TestAStoppedAgentIsStoppedWhatEverTheFeedSays(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	a := running("coder-1", "working")
	a.State = "exited"
	a.Waiting = true
	r.fakes["web"].SetAgent(a)
	op := r.operator()
	eventually(t, "the feed", func() bool { _, feed := r.agentOf(op, r.id("web"), "coder-1"); return feed.State == FeedLive })
	if got, _ := r.agentOf(op, r.id("web"), "coder-1"); got.Activity != "stopped" || got.Waiting {
		t.Errorf("a stopped agent is %+v", got)
	}
}

func TestAQuietSidecarIsStaleAfterThreeHeartbeats(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(running("coder-1", "idle"))
	r.fakes["web"].SetFeedInterval(time.Hour) // one line, then nothing for a long time
	op := r.operator()
	eventually(t, "the first line", func() bool { _, feed := r.agentOf(op, r.id("web"), "coder-1"); return feed.State == FeedLive })
	// pretend the line is old: the follower heard it long ago, and three heartbeats of an hour have not passed, so it is still good
	m := r.hub.feeds
	m.mu.Lock()
	f := m.followers[r.id("web")]
	f.lastLine = time.Now().Add(-2 * time.Hour)
	f.interval = 10 * time.Millisecond
	m.mu.Unlock()
	got, feed := r.agentOf(op, r.id("web"), "coder-1")
	if feed.State != FeedStale || !strings.Contains(feed.Message, "no status") || got.Activity != ActivityUnknown {
		t.Errorf("a silent sidecar: %+v activity %q", feed, got.Activity)
	}
}

func TestALineReplayedWhenTheStreamOpensIsAsOldAsItWas(t *testing.T) {
	r := newRig(t)
	m := r.hub.feeds
	f := &follower{since: time.Now(), agents: map[string]links.AgentStatus{}}
	m.followers["p"] = f
	old := links.StatusLine{Time: time.Now().Add(-time.Hour), Interval: 30 * time.Second, Agents: []links.AgentStatus{{Agent: "coder-1", Activity: "idle"}}}
	m.status("p", old)
	if feed := m.state(f, time.Now()); feed.State != FeedStale {
		t.Errorf("a replayed hour-old line makes the sidecar look %q", feed.State)
	}
	f.since = time.Now().Add(-time.Minute)
	m.status("p", links.StatusLine{Time: time.Now(), Interval: 30 * time.Second, Agents: old.Agents})
	if feed := m.state(f, time.Now()); feed.State != FeedLive {
		t.Errorf("a line that arrives live: %q", feed.State)
	}
}

func TestAPageThatPollsAsksTheLinkForNothing(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(running("coder-1", "idle"))
	op, id := r.operator(), r.id("web")
	reads := f.ProjectReads()
	for range 50 {
		if _, err := r.hub.Project(ctx, op, id); err != nil {
			t.Fatal(err)
		}
		if _, err := r.hub.Projects(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.ProjectReads() - reads; n != 0 {
		t.Errorf("polling read the project %d times", n)
	}
}

func TestWhatTheLinkPushesSaysWhichTemplatesThereAreAndWhichAgentsAreStale(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	a := running("coder-1", "idle")
	a.TemplateHash = "hash-1"
	f.SetAgent(a)
	f.SetPublished(links.Published{Templates: map[string]string{"coder": "hash-1", "reviewer": "hash-r"}})
	op, id := r.operator(), r.id("web")
	view, err := r.hub.Project(ctx, op, id)
	if err != nil || len(view.Project.Templates) != 2 || view.Agents[0].Stale {
		t.Fatalf("view = %+v %+v, %v", view.Project, view.Agents, err)
	}
	f.SetPublished(links.Published{Templates: map[string]string{"coder": "hash-2"}})
	if view, _ = r.hub.Project(ctx, op, id); !view.Agents[0].Stale {
		t.Errorf("an agent of a template that changed = %+v", view.Agents[0])
	}
	f.SetPublished(links.Published{Templates: map[string]string{"reviewer": "hash-r"}})
	if view, _ = r.hub.Project(ctx, op, id); !view.Agents[0].Stale {
		t.Errorf("an agent of a template that is gone = %+v", view.Agents[0])
	}
}

func TestAProjectWhoseControlSidecarStoppedSaysSo(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(running("coder-1", "idle"))
	op, id := r.operator(), r.id("web")
	eventually(t, "the feed", func() bool { _, feed := r.agentOf(op, id, "coder-1"); return feed.State == FeedLive })
	f.SetServices(links.Service{Name: "control", State: "exited"}, links.Service{Name: "proxy", State: "running"})
	got, feed := r.agentOf(op, id, "coder-1")
	if feed.State != FeedUnavailable || !strings.Contains(feed.Message, "not running") || got.Activity != ActivityUnknown {
		t.Errorf("feed %+v, agent %+v", feed, got)
	}
}

func TestAnOperatorSeesTheFeedOfEveryProjectAndAUserOnlyTheirs(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	r.fakes["web"].SetAgent(running("coder-1", "working"))
	r.operator()
	alice := r.login("alice")
	r.store.SetRole(ctx, r.id("web"), alice.ID, rbac.User)
	eventually(t, "the feed", func() bool {
		got, feed := r.agentOf(alice, r.id("web"), "coder-1")
		return feed.State == FeedLive && got.Activity == "working"
	})
}
