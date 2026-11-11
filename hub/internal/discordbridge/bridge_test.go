// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discordbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/bridges"
	"github.com/egzo-ai/egzo/hub/internal/discord"
	"github.com/egzo-ai/egzo/hub/internal/discord/discordtest"
	"github.com/egzo-ai/egzo/hub/internal/discordbridge"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

var ctx = context.Background()

const (
	token  = "bot-token"
	secret = "a-bridge-secret-of-at-least-32-characters"
	guild  = "10"
	room   = "20"
)

type world struct {
	t       *testing.T
	hub     *hub.Hub
	admin   store.User
	project string
	web     *fake.Engine
	discord *discordtest.Server
	state   *discordbridge.State
}

// recorded waits until the bridge knows which egzo message a Discord message is: a person replies or clicks after that, never before.
func (w *world) recorded(message string) {
	w.t.Helper()
	eventually(w.t, "the bridge to record its post", func() bool {
		_, ok, _ := w.state.PostOf(ctx, discord.Snowflake(message))
		return ok
	})
}

func setup(t *testing.T) *world { return setupWith(t, setupOptions{}) }

// setupOptions change the world before the bridge starts.
type setupOptions struct {
	// api wraps the hub's bridge API, as the bridge reaches it.
	api func(http.Handler) http.Handler
	// before runs before the bridge starts.
	before func(*world)
	// config changes the bridge's config.
	config func(*discordbridge.Config)
}

func setupWith(t *testing.T, o setupOptions) *world {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := hub.New(s, nil, hub.FeedOptions{Flush: 5 * time.Millisecond})
	if _, err := h.SetupSignIn(ctx, hub.SignInOptions{LocalLogin: true, AdminPassword: "admin-password"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByName(ctx, store.StarterUsername)
	_, code, _ := h.CreateLinkCode(ctx, admin)
	web := fake.New("web", "laptop", "coder")
	go h.Link(ctx, web.Hello(), code, web.Connect())
	t.Cleanup(web.Drop)
	eventually(t, "the link", web.Served)
	web.SetAgent(links.Agent{Name: "coder-1", Template: "coder", State: "running", Actor: "operator", Created: time.Now()})
	reg, _ := s.RegistrationByKey(ctx, web.Hello().Key)
	var handler http.Handler = bridges.New(h, map[string]string{"discord": secret}, "https://hub.example.org", nil).Handler()
	if o.api != nil {
		handler = o.api(handler)
	}
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	d := discordtest.New(token)
	d.Heartbeat = 200 * time.Millisecond
	t.Cleanup(d.Close)
	d.OnReady(discord.Guild{ID: guild, Name: "team", Channels: []discord.Channel{{ID: room, Type: discord.ChannelText, Name: "general"}},
		Roles: []discord.Role{{ID: guild, Name: "@everyone"}}})
	state, err := discordbridge.OpenState(filepath.Join(t.TempDir(), "discord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	w := &world{t: t, hub: h, admin: admin, project: reg.ID, web: web, discord: d, state: state}
	if o.before != nil {
		o.before(w)
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	cfg := discordbridge.Config{Token: token, API: d.API(), Hub: &discordbridge.HubAPI{URL: api.URL, Secret: secret}, State: state}
	if o.config != nil {
		o.config(&cfg)
	}
	go func() { done <- discordbridge.New(cfg).Run(run) }()
	t.Cleanup(func() { stop(); <-done })
	w.request("the commands", "PUT /applications/2/guilds/"+guild+"/commands")
	return w
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// request waits for the bridge to make a request to Discord, and returns the last one.
func (w *world) request(what, prefix string) discordtest.Request {
	w.t.Helper()
	var got []discordtest.Request
	eventually(w.t, what, func() bool { got = w.discord.Requests(prefix); return len(got) > 0 })
	return got[len(got)-1]
}

// count waits until the bridge made n requests with the prefix.
func (w *world) count(what, prefix string, n int) []discordtest.Request {
	w.t.Helper()
	var got []discordtest.Request
	eventually(w.t, what, func() bool { got = w.discord.Requests(prefix); return len(got) >= n })
	return got
}

func (w *world) interaction(id string, kind int, data any, member discord.Member, message *discord.Message) {
	raw, _ := json.Marshal(data)
	body := map[string]any{"id": id, "application_id": "2", "type": kind, "token": "tok-" + id, "guild_id": guild, "channel_id": room, "member": member, "data": json.RawMessage(raw)}
	if message != nil {
		body["message"] = message
	}
	w.discord.Dispatch("INTERACTION_CREATE", body)
}

func (w *world) calls(prefix string) []string {
	var out []string
	for _, c := range w.web.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func command(sub ...any) map[string]any {
	return map[string]any{"name": "egzo", "type": 1, "options": sub}
}

func TestAPersonLinksAddsAnAgentTalksToItAndAnswersItsQuestionWithAButton(t *testing.T) {
	w := setup(t)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Roles: []discord.Snowflake{}, Permissions: "268435456"} // Manage Roles

	// /egzo register: the bridge answers with the hub's address, the code after its #
	w.interaction("501", discord.InteractionCommand, command(map[string]any{"type": 1, "name": "register"}), joe, nil)
	answer := w.request("the register answer", "PATCH /webhooks/2/tok-501/messages/@original")
	var said struct{ Content string }
	_ = answer.JSON(&said)
	code := regexp.MustCompile(`https://hub\.example\.org/chat-link#(egzo-chat-[a-z2-7]+)`).FindStringSubmatch(said.Content)
	if code == nil {
		t.Fatalf("register said %q", said.Content)
	}
	if _, err := w.hub.ConfirmBridgeCode(ctx, w.admin, code[1]); err != nil { // joe is the admin on the hub, signed in
		t.Fatal(err)
	}

	// /egzo agent add: a role for the agent, in this channel
	add := map[string]any{"type": 2, "name": "agent", "options": []any{map[string]any{"type": 1, "name": "add",
		"options": []any{map[string]any{"type": 3, "name": "agent", "value": w.project + "/coder-1"}}}}}
	w.interaction("502", discord.InteractionCommand, command(add), joe, nil)
	roleReq := w.request("the role", "POST /guilds/"+guild+"/roles")
	var role struct{ ID, Name string }
	added := w.request("the add answer", "PATCH /webhooks/2/tok-502/messages/@original")
	_ = roleReq.JSON(&role)
	var addSaid struct{ Content string }
	_ = added.JSON(&addSaid)
	if !strings.Contains(addSaid.Content, "**coder-1** is in this channel") {
		t.Fatalf("add said %q", addSaid.Content)
	}
	var roleBody map[string]any
	_ = roleReq.JSON(&roleBody)
	if roleBody["name"] != "coder-1" || roleBody["mentionable"] != true {
		t.Errorf("role %v", roleBody)
	}
	roleID := regexp.MustCompile(`<@&(\d+)>`).FindStringSubmatch(addSaid.Content)[1]

	// joe mentions the role: the agent gets it, as joe's hub user
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "100", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "<@&" + roleID + "> deploy it", MentionRoles: []discord.Snowflake{discord.Snowflake(roleID)}})
	eventually(t, "the message to reach the agent", func() bool { return len(w.calls("send coder-1 user:"+w.admin.ID+" 100")) == 1 })
	w.recorded("100") // where the agent's answer goes

	// the agent asks joe a question: it comes back as a reply to joe's message, with buttons, pinging joe only
	w.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + w.admin.ID, Kind: "question",
		Text: "staging or production?", Re: "m00000000000000000000000000000001", ID: "mq", Choices: []string{"staging", "production"}})
	posted := w.request("the question", "POST /channels/"+room+"/messages")
	var body struct {
		Components      json.RawMessage
		Flags           int
		AllowedMentions discord.AllowedMentions   `json:"allowed_mentions"`
		Reference       *discord.MessageReference `json:"message_reference"`
	}
	_ = posted.JSON(&body)
	if !strings.Contains(string(body.Components), `"custom_id":"egzo:c:1"`) || body.Flags != discord.MessageFlagComponentsV2 {
		t.Errorf("question = %s", body.Components)
	}
	if body.Reference == nil || body.Reference.MessageID != "100" || len(body.AllowedMentions.Users) != 1 || body.AllowedMentions.Users[0] != "7" {
		t.Errorf("reference %+v mentions %+v", body.Reference, body.AllowedMentions)
	}

	// joe clicks production: the hub answers the question as joe
	var sent struct{ ID string }
	_ = json.Unmarshal(posted.Response, &sent)
	w.recorded(sent.ID)
	w.interaction("503", discord.InteractionComponent, map[string]any{"custom_id": "egzo:c:1", "component_type": 2}, joe, &discord.Message{ID: discord.Snowflake(sent.ID)})
	eventually(t, "the answer", func() bool { return len(w.calls("resolve mq user:"+w.admin.ID+" production")) == 1 })
	clicked := w.request("the click answer", "PATCH /webhooks/2/tok-503/messages/@original")
	_ = clicked.JSON(&said)
	if !strings.Contains(said.Content, "production") {
		t.Errorf("the click said %q", said.Content)
	}

	// someone who never linked is told how, and nothing goes to the agent
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "101", ChannelID: room, GuildID: guild, Author: discord.User{ID: "8", Username: "ann"},
		Content: "<@&" + roleID + "> hi", MentionRoles: []discord.Snowflake{discord.Snowflake(roleID)}})
	hints := w.count("the hint", "POST /channels/"+room+"/messages", 2)
	var hint struct{ Content string }
	_ = hints[len(hints)-1].JSON(&hint)
	if !strings.Contains(hint.Content, "/egzo register") || len(w.calls("send coder-1")) != 1 {
		t.Errorf("hint %q, sends %v", hint.Content, w.calls("send"))
	}

	// the agent is removed from its project: its role stays, and a mention of it is told the agent is not running
	w.web.RemoveAgent("coder-1")
	w.gone("coder-1", true)
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "102", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "<@&" + roleID + "> still there?", MentionRoles: []discord.Snowflake{discord.Snowflake(roleID)}})
	replies := w.count("the reply", "POST /channels/"+room+"/messages", 3)
	var reply struct{ Content string }
	_ = replies[len(replies)-1].JSON(&reply)
	if !strings.Contains(reply.Content, "**coder-1** is not running") || !strings.Contains(reply.Content, "re-created under the same name") {
		t.Errorf("a mention of a removed agent got %q", reply.Content)
	}

	// re-created under the same name, it has its role and channel back: a mention reaches the new one
	w.web.SetAgent(links.Agent{Name: "coder-1", Template: "coder", State: "running", Actor: "operator", Created: time.Now()})
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "103", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "<@&" + roleID + "> welcome back", MentionRoles: []discord.Snowflake{discord.Snowflake(roleID)}})
	eventually(t, "the message to reach the new agent", func() bool { return len(w.calls("send coder-1 user:"+w.admin.ID+" 103")) == 1 })
	w.gone("coder-1", false)
	if deleted := w.discord.Requests("DELETE /guilds/" + guild + "/roles/"); len(deleted) != 0 {
		t.Errorf("a role was deleted: %v", deleted)
	}
}

// gone waits until the bridge knows whether an agent left its project.
func (w *world) gone(agent string, gone bool) {
	w.t.Helper()
	eventually(w.t, fmt.Sprintf("%s gone=%v", agent, gone), func() bool {
		a, ok, _ := w.state.AgentInGuild(ctx, guild, w.project, agent)
		return ok && a.Gone.IsZero() != gone
	})
}

// quick is a bridge that sweeps often and keeps a removed agent's role a moment.
func quick(retention time.Duration) setupOptions {
	return setupOptions{config: func(c *discordbridge.Config) { c.Retention, c.SweepEvery = retention, 20*time.Millisecond }}
}

func TestARemovedAgentLosesItsRoleOnceTheRetentionIsOver(t *testing.T) {
	w := setupWith(t, quick(300*time.Millisecond))
	role := w.linked(discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"})
	w.web.RemoveAgent("coder-1")
	w.gone("coder-1", true)
	w.request("the role deleted", "DELETE /guilds/"+guild+"/roles/"+role)
	eventually(t, "the agent forgotten", func() bool {
		_, ok, _ := w.state.AgentInGuild(ctx, guild, w.project, "coder-1")
		return !ok
	})
}

func TestAnAgentTheHubStillHasKeepsItsRoleWhateverTheBridgeThought(t *testing.T) {
	w := setupWith(t, quick(time.Millisecond))
	w.linked(discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"})
	// a removal long ago, and the agent re-created while the bridge was away: the hub says it is there
	if err := w.state.MarkGone(ctx, w.project, "coder-1", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.gone("coder-1", false)
	time.Sleep(100 * time.Millisecond)
	if deleted := w.discord.Requests("DELETE /guilds/" + guild + "/roles/"); len(deleted) != 0 {
		t.Errorf("the role of an agent that is there was deleted: %v", deleted)
	}
}

// withoutRemovals is a hub whose stream never says an agent was removed: as one that restarted while the agent went.
type withoutRemovals struct {
	http.ResponseWriter
	partial []byte
}

func (w *withoutRemovals) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := strings.IndexByte(string(w.partial), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := w.partial[:i+1]
		w.partial = w.partial[i+1:]
		if !strings.Contains(string(line), `"agent.removed"`) {
			if _, err := w.ResponseWriter.Write(line); err != nil {
				return 0, err
			}
		}
	}
}

func (w *withoutRemovals) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func TestAnAgentRemovedThatTheHubNeverToldIsFoundBySweeping(t *testing.T) {
	o := quick(300 * time.Millisecond)
	o.api = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages") {
				w = &withoutRemovals{ResponseWriter: w}
			}
			next.ServeHTTP(w, r)
		})
	}
	w := setupWith(t, o)
	role := w.linked(discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"})
	w.web.RemoveAgent("coder-1")
	w.gone("coder-1", true)
	w.request("the role deleted", "DELETE /guilds/"+guild+"/roles/"+role)
}

// linked is joe, registered and with coder-1 added to the channel: the role's id.
func (w *world) linked(joe discord.Member) string {
	w.t.Helper()
	w.interaction("401", discord.InteractionCommand, command(map[string]any{"type": 1, "name": "register"}), joe, nil)
	var said struct{ Content string }
	_ = w.request("register", "PATCH /webhooks/2/tok-401/messages/@original").JSON(&said)
	code := regexp.MustCompile(`#(egzo-chat-[a-z2-7]+)`).FindStringSubmatch(said.Content)
	if _, err := w.hub.ConfirmBridgeCode(ctx, w.admin, code[1]); err != nil {
		w.t.Fatal(err)
	}
	add := map[string]any{"type": 2, "name": "agent", "options": []any{map[string]any{"type": 1, "name": "add",
		"options": []any{map[string]any{"type": 3, "name": "agent", "value": w.project + "/coder-1"}}}}}
	w.interaction("402", discord.InteractionCommand, command(add), joe, nil)
	_ = w.request("add", "PATCH /webhooks/2/tok-402/messages/@original").JSON(&said)
	return regexp.MustCompile(`<@&(\d+)>`).FindStringSubmatch(said.Content)[1]
}

func TestWhatAnAgentStartsGoesToItsHomeChannelAndAReplyToItAnswersIt(t *testing.T) {
	w := setup(t)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"} // an administrator
	w.linked(joe)
	w.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + w.admin.ID, Kind: "request", Text: "may I merge?", ID: "mr"})
	posted := w.request("the request", "POST /channels/"+room+"/messages")
	var body struct {
		Components json.RawMessage
		Reference  *discord.MessageReference `json:"message_reference"`
	}
	_ = posted.JSON(&body)
	if body.Reference != nil || !strings.Contains(string(body.Components), "<@7>") || !strings.Contains(string(body.Components), "may I merge?") {
		t.Errorf("posted %s, reference %+v", body.Components, body.Reference)
	}
	var sent struct{ ID string }
	_ = json.Unmarshal(posted.Response, &sent)
	w.recorded(sent.ID)
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "300", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "yes, merge", MessageReference: &discord.MessageReference{MessageID: discord.Snowflake(sent.ID)}})
	eventually(t, "the answer", func() bool { return len(w.calls("resolve mr user:"+w.admin.ID+" yes, merge")) == 1 })
}

func TestSayIsAPublicMessageWhatTheAgentAnswersRepliesToIt(t *testing.T) {
	w := setup(t)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"}
	w.linked(joe)
	say := map[string]any{"type": 1, "name": "say", "options": []any{
		map[string]any{"type": 3, "name": "agent", "value": w.project + "/coder-1"}, map[string]any{"type": 3, "name": "text", "value": "status?"}}}
	w.interaction("601", discord.InteractionCommand, command(say), joe, nil)
	callback := w.request("the deferral", "POST /interactions/601/tok-601/callback")
	var deferral struct {
		Type int
		Data struct{ Flags int }
	}
	_ = callback.JSON(&deferral)
	if deferral.Type != discord.ResponseDeferredMessage || deferral.Data.Flags&discord.Ephemeral != 0 {
		t.Errorf("say is answered in public: %+v", deferral)
	}
	eventually(t, "the message", func() bool { return len(w.calls("send coder-1 user:"+w.admin.ID+" 601")) == 1 })
	original := w.request("the answer", "PATCH /webhooks/2/tok-601/messages/@original")
	var shown struct{ ID string }
	_ = json.Unmarshal(original.Response, &shown)
	w.recorded(shown.ID)
	// the fake link numbers the messages it is sent: the first is m000…001, what the agent's result names
	w.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + w.admin.ID, Kind: "resolution", Outcome: "done",
		Text: "all green", Re: "m00000000000000000000000000000001", ID: "mres"})
	posted := w.request("the result", "POST /channels/"+room+"/messages")
	var body struct {
		Reference *discord.MessageReference `json:"message_reference"`
	}
	_ = posted.JSON(&body)
	if body.Reference == nil || string(body.Reference.MessageID) != shown.ID {
		t.Errorf("the result replies to %+v, want %s", body.Reference, shown.ID)
	}
}

func TestAChannelTheBotCanNoLongerSeeLosesItsAgents(t *testing.T) {
	w := setup(t)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"}
	role := w.linked(joe)
	w.discord.Dispatch("CHANNEL_UPDATE", discord.Channel{ID: room, GuildID: guild, Type: discord.ChannelText, Name: "___hidden___", Flags: discord.ChannelObfuscated})
	time.Sleep(50 * time.Millisecond)
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "700", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "<@&" + role + "> hello", MentionRoles: []discord.Snowflake{discord.Snowflake(role)}})
	time.Sleep(100 * time.Millisecond)
	if sends := w.calls("send"); len(sends) != 0 {
		t.Errorf("a message in a channel the agent left went to it: %v", sends)
	}
}

func TestABridgeThatStartsBeforeTheHubStillPostsToPeopleLinkedBefore(t *testing.T) {
	var down, streaming atomic.Bool
	down.Store(true)
	w := setupWith(t, setupOptions{
		api: func(next http.Handler) http.Handler { // the hub is not up yet when the bridge starts
			return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if down.Load() {
					http.Error(rw, "starting", http.StatusBadGateway)
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == bridges.Prefix+"/messages" {
					streaming.Store(true)
				}
				next.ServeHTTP(rw, r)
			})
		},
		before: func(w *world) { // joe linked his account, and coder-1 is in the channel, from an earlier run of the bridge
			code, _, err := w.hub.BridgeCode(ctx, "discord", "7", "joe")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.hub.ConfirmBridgeCode(ctx, w.admin, code); err != nil {
				t.Fatal(err)
			}
			a := discordbridge.Agent{Guild: guild, Role: "900", Project: w.project, Agent: "coder-1", Name: "coder-1", Home: room}
			if err := w.state.AddAgent(ctx, a); err != nil {
				t.Fatal(err)
			}
			if err := w.state.AddPlace(ctx, guild, "900", room); err != nil {
				t.Fatal(err)
			}
		},
	})
	down.Store(false)
	eventually(t, "the bridge to read the hub's outbox", streaming.Load)
	w.web.PushEvent(links.Event{Type: "message", Agent: "coder-1", Actor: "agent:coder-1", To: "user:" + w.admin.ID, Kind: "request", Text: "may I merge?", ID: "mr"})
	posted := w.request("the request", "POST /channels/"+room+"/messages")
	if !strings.Contains(string(posted.Body), "may I merge?") || !strings.Contains(string(posted.Body), "<@7>") {
		t.Errorf("posted %s", posted.Body)
	}
}

// hanging is a hub that holds the calls to some routes until the test ends.
type hanging struct {
	routes  sync.Map // such as "POST /bridge/v1/messages": true
	held    atomic.Int32
	release chan struct{}
}

func hang() (*hanging, setupOptions) {
	h := &hanging{release: make(chan struct{})}
	return h, setupOptions{api: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if _, held := h.routes.Load(r.Method + " " + r.URL.Path); held {
				h.held.Add(1)
				select {
				case <-h.release:
				case <-r.Context().Done():
				}
				return
			}
			next.ServeHTTP(rw, r)
		})
	}}
}

// arm holds the routes from now on; until the test ends.
func (h *hanging) arm(t *testing.T, routes ...string) {
	for _, r := range routes {
		h.routes.Store(r, true)
	}
	t.Cleanup(func() { close(h.release) }) // before the hub's server closes: it waits for the calls it holds
}

func say(project, text string) map[string]any {
	return command(map[string]any{"type": 1, "name": "say", "options": []any{
		map[string]any{"type": 3, "name": "agent", "value": project + "/coder-1"}, map[string]any{"type": 3, "name": "text", "value": text}}})
}

// answered is the answer to an interaction: its type, and what it says.
func (w *world) answered(id string) (int, string) {
	w.t.Helper()
	var r struct {
		Type int
		Data json.RawMessage
	}
	_ = w.request("the answer to "+id, "POST /interactions/"+id+"/tok-"+id+"/callback").JSON(&r)
	return r.Type, string(r.Data)
}

func TestACommandIsAnsweredWhileTheHubHoldsAMessage(t *testing.T) {
	h, o := hang()
	w := setupWith(t, o)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"}
	role := w.linked(joe)
	h.arm(t, "POST "+bridges.Prefix+"/messages")
	w.discord.Dispatch("MESSAGE_CREATE", discord.Message{ID: "800", ChannelID: room, GuildID: guild, Author: discord.User{ID: "7", Username: "joe"},
		Content: "<@&" + role + "> status?", MentionRoles: []discord.Snowflake{discord.Snowflake(role)}})
	eventually(t, "the hub to hold the message", func() bool { return h.held.Load() == 1 })
	w.interaction("801", discord.InteractionCommand, command(map[string]any{"type": 1, "name": "register"}), joe, nil)
	if kind, _ := w.answered("801"); kind != discord.ResponseDeferredMessage {
		t.Errorf("answered %d", kind)
	}
	w.request("the code", "PATCH /webhooks/2/tok-801/messages/@original")
}

func TestPastTwoInteractionsAPersonIsToldTheirEarlierOnesStillRun(t *testing.T) {
	h, o := hang()
	w := setupWith(t, o)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}} // no earlier interaction of his is still finishing
	h.arm(t, "POST "+bridges.Prefix+"/messages")
	w.interaction("811", discord.InteractionCommand, say(w.project, "one"), joe, nil)
	w.interaction("812", discord.InteractionCommand, say(w.project, "two"), joe, nil)
	eventually(t, "the hub to hold both", func() bool { return h.held.Load() == 2 })
	w.interaction("813", discord.InteractionAutocomplete, say(w.project, "thr"), joe, nil)
	if kind, data := w.answered("813"); kind != discord.ResponseAutocomplete || !strings.Contains(data, "Busy, try again") {
		t.Errorf("answered %d %s", kind, data)
	}
	w.interaction("814", discord.InteractionCommand, say(w.project, "three"), joe, nil)
	if kind, data := w.answered("814"); kind != discord.ResponseMessage || !strings.Contains(data, "Your earlier commands are still running") ||
		!strings.Contains(data, `"flags":64`) {
		t.Errorf("answered %d %s", kind, data)
	}
	ann := discord.Member{User: &discord.User{ID: "8", Username: "ann"}}
	w.interaction("815", discord.InteractionCommand, command(map[string]any{"type": 1, "name": "register"}), ann, nil)
	w.request("ann's code", "PATCH /webhooks/2/tok-815/messages/@original") // joe's limit is joe's
}

func TestPastSixteenInteractionsOneMoreIsToldTheBridgeIsBusy(t *testing.T) {
	h, o := hang()
	w := setupWith(t, o)
	h.arm(t, "POST "+bridges.Prefix+"/messages")
	for n := range 16 {
		id := strconv.Itoa(820 + n)
		w.interaction(id, discord.InteractionCommand, say(w.project, "hi"), discord.Member{User: &discord.User{ID: discord.Snowflake(id), Username: "p" + id}}, nil)
	}
	eventually(t, "the hub to hold 16", func() bool { return h.held.Load() == 16 })
	w.interaction("850", discord.InteractionCommand, say(w.project, "hi"), discord.Member{User: &discord.User{ID: "850", Username: "late"}}, nil)
	if kind, data := w.answered("850"); kind != discord.ResponseMessage || !strings.Contains(data, "egzo is busy") {
		t.Errorf("answered %d %s", kind, data)
	}
}

func TestAnAutocompleteTheHubDoesNotAnswerInTimeSaysSo(t *testing.T) {
	h, o := hang()
	w := setupWith(t, o)
	joe := discord.Member{User: &discord.User{ID: "7", Username: "joe"}, Permissions: "8"}
	w.linked(joe)
	h.arm(t, "GET "+bridges.Prefix+"/agents")
	start := time.Now()
	w.interaction("860", discord.InteractionAutocomplete, say(w.project, "st"), joe, nil)
	kind, data := w.answered("860")
	if kind != discord.ResponseAutocomplete || !strings.Contains(data, "The hub is not answering") {
		t.Errorf("answered %d %s", kind, data)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("answered after %s: Discord gives up at 3 s", took)
	}
}
