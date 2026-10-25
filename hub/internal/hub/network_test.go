package hub

import (
	"context"
	"fmt"
	"hash/fnv"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// flushing is quick, with what an audit follower heard written at once.
var flushing = func() FeedOptions { o := quick; o.Flush = 5 * time.Millisecond; return o }()

// trail makes the audit events of one agent, a second apart.
type trail struct {
	agent, instance, profile string
	at                       time.Time
}

func trailOf(a engines.Agent) *trail {
	return &trail{agent: a.Name, instance: a.ID, profile: "default", at: time.Now().Add(-time.Minute).UTC()}
}

func (t *trail) next(e engines.AuditEvent) engines.AuditEvent {
	t.at = t.at.Add(time.Second)
	e.Time, e.Agent, e.Instance, e.Profile = t.at, t.agent, t.instance, t.profile
	id := fnv.New64a() // a connection's id, as the proxy makes them: 16 hex digits
	id.Write([]byte(t.agent + t.at.Format(time.RFC3339Nano)))
	e.Conn = fmt.Sprintf("%016x", id.Sum64())
	if e.Protocol == "" {
		e.Protocol = proxy.HTTPS
	}
	return e
}

func (t *trail) denied(host string, code proxy.Code) engines.AuditEvent {
	return t.next(engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeDeny, Host: host, Reason: &proxy.Reason{Code: code, Text: string(code)}})
}

func (t *trail) allowed(host string) engines.AuditEvent {
	return t.next(engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeAllow, Mode: proxy.ModeTunnel, Host: host})
}

func (r *rig) agentNamed(project, name string) engines.Agent {
	r.t.Helper()
	p, err := r.fakes["local"].Project(ctx, project)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, a := range p.Agents {
		if a.Name == name {
			return a
		}
	}
	r.t.Fatalf("no agent %s", name)
	return engines.Agent{}
}

func (r *rig) networkOf(user store.User, agent string) NetworkView {
	r.t.Helper()
	view, err := r.hub.Network(ctx, user, "local", "web", agent)
	if err != nil {
		r.t.Fatal(err)
	}
	return view
}

func groups(view NetworkView) map[string]store.Denial {
	out := map[string]store.Denial{}
	for _, d := range view.Denials {
		out[d.Host+" "+d.Code] = d
	}
	return out
}

func TestDenialsAreGroupedByInstanceHostAndReasonAndAHostReachedSinceIsFixed(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	if _, err := r.hub.Spawn(ctx, op, "local", "web", "coder", ""); err != nil {
		t.Fatal(err)
	}
	r.startFeeds(flushing)
	r.scan()
	a := trailOf(r.agentNamed("web", "coder-1"))
	r.fakes["local"].AddAudit("web",
		a.denied("registry.npmjs.org", proxy.CodeNotInProfile), a.denied("registry.npmjs.org", proxy.CodeNotInProfile),
		a.denied("db.internal", proxy.CodePort), a.denied("registry.npmjs.org", proxy.CodeNotInProfile),
		a.next(engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeError, Host: "api.example.com", Reason: &proxy.Reason{Code: proxy.CodeAborted}}),
		a.next(engines.AuditEvent{Kind: proxy.KindConnect, Decision: proxy.OutcomeChallenge, Host: "example.com", Reason: &proxy.Reason{Code: proxy.CodeNoCredentials}}),
	)
	eventually(t, "the denials to be grouped", func() bool {
		g := groups(r.networkOf(op, ""))
		return g["registry.npmjs.org not-in-profile"].Count == 3 && g["db.internal port"].Count == 1
	})
	view := r.networkOf(op, "")
	if len(view.Denials) != 2 {
		t.Errorf("a client that left, or a challenge, is not a denial: %+v", view.Denials)
	}
	npm := groups(view)["registry.npmjs.org not-in-profile"]
	if npm.Agent != "coder-1" || npm.Profile != "default" || npm.Actor != rbac.Actor(op.ID) || npm.Event != "connect" || !npm.Fixed.IsZero() {
		t.Errorf("group = %+v", npm)
	}
	if len(view.Events) != 6 || view.Feed.State != FeedLive {
		t.Errorf("the log keeps every event: %d, feed %+v", len(view.Events), view.Feed)
	}
	if got, _ := r.agentOf(op, "local", "web", "coder-1"); got.Name == "" {
		t.Fatal("no agent")
	}
	if p, _ := r.hub.Project(ctx, op, "local", "web"); p.Agents[0].Denied != 2 {
		t.Errorf("denied hosts = %d, want 2", p.Agents[0].Denied)
	}

	// the profile was changed: the agent reaches the host, and the group says it is fixed
	r.fakes["local"].AddAudit("web", a.allowed("registry.npmjs.org"))
	eventually(t, "the group to be fixed", func() bool { return !groups(r.networkOf(op, ""))["registry.npmjs.org not-in-profile"].Fixed.IsZero() })
	if p, _ := r.hub.Project(ctx, op, "local", "web"); p.Agents[0].Denied != 1 {
		t.Errorf("denied hosts = %d, want 1: a fixed host does not count", p.Agents[0].Denied)
	}
	// and a new refusal opens it again
	r.fakes["local"].AddAudit("web", a.denied("registry.npmjs.org", proxy.CodeNotInProfile))
	eventually(t, "the group to be open again", func() bool {
		g := groups(r.networkOf(op, ""))["registry.npmjs.org not-in-profile"]
		return g.Fixed.IsZero() && g.Count == 4
	})
}

func TestANameThatComesBackIsAnotherInstanceWithNoDenials(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	r.hub.Spawn(ctx, op, "local", "web", "coder", "issue-9")
	r.startFeeds(flushing)
	r.scan()
	first := trailOf(r.agentNamed("web", "issue-9"))
	r.fakes["local"].AddAudit("web", first.denied("evil.example", proxy.CodeNotInProfile))
	eventually(t, "the denial", func() bool { return len(r.networkOf(op, "issue-9").Denials) == 1 })

	if err := r.hub.Operate(ctx, op, "rm", "local", "web", "issue-9"); err != nil {
		t.Fatal(err)
	}
	r.hub.Spawn(ctx, op, "local", "web", "coder", "issue-9")
	r.scan()
	second := r.agentNamed("web", "issue-9")
	if second.ID == first.instance {
		t.Fatal("the fake reused an id")
	}
	if view := r.networkOf(op, "issue-9"); len(view.Denials) != 0 || len(view.Events) != 0 {
		t.Errorf("the new issue-9 has the old one's history: %+v", view)
	}
	if view := r.networkOf(op, ""); len(view.Denials) != 1 || view.Denials[0].Instance != first.instance {
		t.Errorf("the project keeps the removed instance's denials: %+v", view.Denials)
	}
	if p, _ := r.hub.Project(ctx, op, "local", "web"); p.Agents[0].Denied != 0 {
		t.Errorf("the new instance counts the old one's denials: %d", p.Agents[0].Denied)
	}
}

func TestAUserSeesTheNetworkOfTheAgentsTheyMayOperateAndAnAdministratorAllOfIt(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	alice, bob, carol := r.login("alice"), r.login("bob"), r.login("carol")
	r.scan()
	r.store.SetRole(ctx, "local", "web", alice.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", bob.ID, rbac.User)
	r.store.SetRole(ctx, "local", "web", carol.ID, rbac.Administrator)
	r.hub.Spawn(ctx, alice, "local", "web", "coder", "")
	r.startFeeds(flushing)
	r.scan()
	a := trailOf(r.agentNamed("web", "coder-1"))
	stranger := &trail{agent: "ghost", at: a.at} // wrong credentials: the proxy cannot tell who it is
	r.fakes["local"].AddAudit("web", a.denied("evil.example", proxy.CodeNotInProfile), stranger.denied("example.com", proxy.CodeCredentials))
	eventually(t, "both denials", func() bool { return len(r.networkOf(carol, "").Denials) == 2 })

	if view := r.networkOf(alice, ""); len(view.Denials) != 1 || view.Denials[0].Host != "evil.example" || len(view.Events) != 1 {
		t.Errorf("alice sees %+v", view)
	}
	if view := r.networkOf(bob, ""); len(view.Denials) != 0 || len(view.Events) != 0 {
		t.Errorf("bob sees another user's agent: %+v", view)
	}
	if view := r.networkOf(bob, "coder-1"); len(view.Denials) != 0 || len(view.Events) != 0 {
		t.Errorf("bob sees alice's agent by its name: %+v", view)
	}
	if view := r.networkOf(carol, "coder-1"); len(view.Denials) != 1 || len(view.Events) != 1 {
		t.Errorf("one agent's view is that agent's: %+v", view)
	}
	if p, _ := r.hub.Project(ctx, bob, "local", "web"); p.Agents[0].Denied != 0 {
		t.Error("bob is told how many hosts alice's agent was denied")
	}
	if _, err := r.hub.Network(ctx, carol, "local", "web", "nobody"); err != ErrNotFound {
		t.Errorf("an agent that does not exist: %v", err)
	}
	if _, err := r.hub.Network(ctx, r.login("dave"), "local", "web", ""); err != ErrNotFound {
		t.Errorf("a user with no role: %v", err)
	}
}

func TestTheHubReadsTheTrailAgainFromWhereItStoppedAndCountsNothingTwice(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	r.hub.Spawn(ctx, op, "local", "web", "coder", "")
	a := trailOf(r.agentNamed("web", "coder-1"))
	r.fakes["local"].AddAudit("web", a.denied("evil.example", proxy.CodeNotInProfile), a.denied("evil.example", proxy.CodeNotInProfile))

	feeds, stop := context.WithCancel(context.Background())
	r.hub.StartFeeds(feeds, flushing)
	r.scan()
	eventually(t, "the first two", func() bool { return groups(r.networkOf(op, ""))["evil.example not-in-profile"].Count == 2 })
	stop()
	eventually(t, "the cursor to be written", func() bool {
		at, _ := r.store.AuditCursor(ctx, "local", "web")
		return at.Equal(a.at)
	})

	// the hub is down while the agent keeps trying; another hub process starts on the same database
	r.fakes["local"].AddAudit("web", a.denied("evil.example", proxy.CodeNotInProfile))
	r.hub = New(r.store, r.hub.registry, nil)
	r.startFeeds(flushing)
	r.scan()
	eventually(t, "the third", func() bool { return groups(r.networkOf(op, ""))["evil.example not-in-profile"].Count == 3 })
	follows := r.fakes["local"].AuditFollows("web")
	if len(follows) != 2 || !follows[0].IsZero() || follows[1].IsZero() {
		t.Errorf("the second hub did not read from the cursor: %v", follows)
	}
	time.Sleep(20 * time.Millisecond)
	if got := groups(r.networkOf(op, ""))["evil.example not-in-profile"].Count; got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

func TestAFollowerKeepsABoundedNumberOfOpenGroupsAndForgetsTheOldest(t *testing.T) {
	f := &auditFollower{open: map[hostKey]map[string]openGroup{}}
	start := time.Now()
	for i := range maxOpen + 5 {
		f.opened(groupKey{"i", "", "https", fmt.Sprintf("h%d.example", i), "not-in-profile"}, openGroup{event: proxy.KindConnect, last: start.Add(time.Duration(i) * time.Second)})
	}
	n := 0
	for _, codes := range f.open {
		n += len(codes)
	}
	if n != maxOpen {
		t.Fatalf("%d open groups, want %d", n, maxOpen)
	}
	if f.open[hostKey{"i", "https", "h0.example"}] != nil || f.open[hostKey{"i", "https", fmt.Sprintf("h%d.example", maxOpen+4)}] == nil {
		t.Error("not the oldest forgotten")
	}
}
