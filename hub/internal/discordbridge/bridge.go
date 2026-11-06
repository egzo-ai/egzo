// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package discordbridge is egzo-discord: the bridge between Discord and the hub's agents (docs/hub-bridges.md). An agent is added to a
// channel as a role people mention, which the bot creates and nobody holds. A mention of the role, a reply to what the agent posted, or a
// message in a thread it started goes to the agent; what the agent says to a person comes back where they spoke to it, or in its home
// channel, mentioning them. The hub checks every message; the bridge vets Discord's side.
package discordbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/discord"
)

// Config is what the bridge is told at start.
type Config struct {
	Token  string // the bot's token
	Hub    *HubAPI
	State  *State
	Log    *slog.Logger
	API    string // Discord's API; a fake's in tests
	Bridge string // the bridge's name on the hub: discord
}

// Bridge is egzo-discord.
type Bridge struct {
	cfg   Config
	rest  *discord.REST
	hub   *HubAPI
	state *State
	log   *slog.Logger

	mu       sync.Mutex
	app      discord.Snowflake
	accounts map[discord.Snowflake]Account // by Discord id
	byUser   map[string]Account            // by hub user id
	roles    map[discord.Snowflake]map[discord.Snowflake]discord.Role
	channels map[discord.Snowflake]discord.Channel
	hinted   map[discord.Snowflake]time.Time // people told to link their account, and when
	asked    map[discord.Snowflake]time.Time // people the hub was asked about, and when
}

// New makes the bridge.
func New(cfg Config) *Bridge {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.API == "" {
		cfg.API = discord.DefaultURL
	}
	if cfg.Bridge == "" {
		cfg.Bridge = "discord"
	}
	return &Bridge{cfg: cfg, rest: discord.NewREST(cfg.Token, cfg.API, nil), hub: cfg.Hub, state: cfg.State, log: cfg.Log,
		accounts: map[discord.Snowflake]Account{}, byUser: map[string]Account{}, roles: map[discord.Snowflake]map[discord.Snowflake]discord.Role{},
		channels: map[discord.Snowflake]discord.Channel{}, hinted: map[discord.Snowflake]time.Time{}, asked: map[discord.Snowflake]time.Time{}}
}

// Run keeps the bridge up until ctx ends, or Discord refuses the bot in a way that trying again cannot fix.
func (b *Bridge) Run(ctx context.Context) error {
	app, err := b.rest.Application(ctx)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	gw, err := b.rest.GatewayBot(ctx)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	b.mu.Lock()
	b.app = app.ID
	b.mu.Unlock()
	if err := b.refreshAccounts(ctx); err != nil {
		b.log.Warn("read the linked accounts", "err", err)
	}
	events := make(chan discord.Event, 256)
	gateway := &discord.Gateway{Token: b.cfg.Token, Intents: discord.IntentGuilds | discord.IntentGuildMessages | discord.IntentMessageContent,
		URL: gw.URL, Handler: func(e discord.Event) { events <- e }, Log: b.log, Sessions: b.rest.GatewayBot}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 1)
	go func() { failed <- gateway.Run(ctx) }()
	go b.outbound(ctx)
	go b.tidy(ctx)
	b.log.Info("egzo-discord is up", "application", app.Name)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			return err
		case e := <-events:
			b.handle(ctx, e)
		}
	}
}

// tidy forgets posts nobody will reply to any more.
func (b *Bridge) tidy(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		_ = b.state.PurgePosts(ctx, time.Now().Add(-30*24*time.Hour))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (b *Bridge) refreshAccounts(ctx context.Context) error {
	list, err := b.hub.Accounts(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.accounts, b.byUser = map[discord.Snowflake]Account{}, map[string]Account{}
	for _, a := range list {
		b.accounts[discord.Snowflake(a.Account)] = a
		b.byUser[a.User] = a
	}
	return nil
}

// handle is one gateway event.
func (b *Bridge) handle(ctx context.Context, e discord.Event) {
	switch e.Type {
	case "GUILD_CREATE":
		var g discord.Guild
		if json.Unmarshal(e.Data, &g) != nil || g.Unavailable {
			return
		}
		b.mu.Lock()
		b.roles[g.ID] = map[discord.Snowflake]discord.Role{}
		for _, r := range g.Roles {
			b.roles[g.ID][r.ID] = r
		}
		for _, c := range append(g.Channels, g.Threads...) {
			c.GuildID = g.ID
			b.channels[c.ID] = c
		}
		app := b.app
		b.mu.Unlock()
		if err := b.rest.SetGuildCommands(ctx, app, g.ID, commands()); err != nil {
			b.log.Warn("register the commands", "guild", g.Name, "err", err)
		}
		b.log.Info("in a guild", "guild", g.Name, "id", g.ID)
	case "GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE":
		var d struct {
			GuildID discord.Snowflake `json:"guild_id"`
			Role    discord.Role      `json:"role"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			b.mu.Lock()
			if b.roles[d.GuildID] == nil {
				b.roles[d.GuildID] = map[discord.Snowflake]discord.Role{}
			}
			b.roles[d.GuildID][d.Role.ID] = d.Role
			b.mu.Unlock()
		}
	case "GUILD_ROLE_DELETE":
		var d struct {
			GuildID discord.Snowflake `json:"guild_id"`
			RoleID  discord.Snowflake `json:"role_id"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			b.mu.Lock()
			delete(b.roles[d.GuildID], d.RoleID)
			b.mu.Unlock()
			_ = b.state.RemoveAgent(ctx, d.GuildID, d.RoleID) // somebody deleted an agent's role: it is out of the guild
		}
	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "THREAD_CREATE", "THREAD_UPDATE":
		var c discord.Channel
		if json.Unmarshal(e.Data, &c) != nil {
			return
		}
		b.mu.Lock()
		b.channels[c.ID] = c
		b.mu.Unlock()
		if c.Obfuscated() { // the bot can no longer see it: the agents in it leave
			if _, err := b.state.RemoveChannel(ctx, c.GuildID, c.ID); err == nil {
				b.log.Warn("lost a channel the bot cannot view any more: its agents left it", "guild", c.GuildID, "channel", c.ID)
			}
		}
	case "CHANNEL_DELETE", "THREAD_DELETE":
		var c discord.Channel
		if json.Unmarshal(e.Data, &c) == nil {
			b.mu.Lock()
			delete(b.channels, c.ID)
			b.mu.Unlock()
			_, _ = b.state.RemoveChannel(ctx, c.GuildID, c.ID)
		}
	case "MESSAGE_CREATE":
		var m discord.Message
		if json.Unmarshal(e.Data, &m) == nil {
			b.onMessage(ctx, m)
		}
	case "INTERACTION_CREATE":
		var i discord.Interaction
		if json.Unmarshal(e.Data, &i) == nil {
			b.onInteraction(ctx, i)
		}
	}
}

// place is the channel an agent is added to for a message in channel: the channel, or a thread's parent.
func (b *Bridge) place(channel discord.Snowflake) (discord.Snowflake, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.channels[channel]
	if ok && (c.Type == discord.ChannelPublicThread || c.Type == discord.ChannelPrivateThread) && c.ParentID != "" {
		return c.ParentID, true
	}
	return channel, false
}

func (b *Bridge) linked(user discord.Snowflake) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.accounts[user]
	return ok
}

// target is an agent a message goes to, and the question it answers if it is an answer.
type target struct {
	agent  Agent
	answer string
}

// onMessage sends a person's message to the agents it addresses: those whose role it mentions in a channel they are in, the agent whose
// message it replies to, or the agent of the thread it is in. Anything else is not for the bridge.
func (b *Bridge) onMessage(ctx context.Context, m discord.Message) {
	if m.Author.Bot || m.GuildID == "" {
		return
	}
	place, inThread := b.place(m.ChannelID)
	var targets []target
	seen := map[string]bool{}
	add := func(t target) {
		key := t.agent.Project + "/" + t.agent.Agent
		if !seen[key] {
			seen[key] = true
			targets = append(targets, t)
		}
	}
	for _, role := range m.MentionRoles {
		a, ok, _ := b.state.AgentByRole(ctx, m.GuildID, role)
		if !ok {
			continue
		}
		if in, _ := b.state.InPlace(ctx, m.GuildID, role, place); in {
			add(target{agent: a})
		}
	}
	if m.MessageReference != nil && m.MessageReference.MessageID != "" {
		if p, ok, _ := b.state.PostOf(ctx, m.MessageReference.MessageID); ok {
			t := target{agent: Agent{Guild: p.Guild, Project: p.Project, Agent: p.Agent}}
			if p.Kind == "question" || p.Kind == "request" {
				t.answer = p.Egzo // a reply to what an agent asked answers it
			}
			add(t)
		}
	}
	if inThread {
		if p, ok, _ := b.state.PostOf(ctx, m.ChannelID); ok { // a thread started from a message has its id
			add(target{agent: Agent{Guild: p.Guild, Project: p.Project, Agent: p.Agent}})
		}
	}
	if len(targets) == 0 {
		return
	}
	if !b.known(ctx, m.Author.ID) {
		b.hint(ctx, m)
		return
	}
	text := strings.TrimSpace(stripRoles(m.Content, m.MentionRoles))
	if text == "" {
		return
	}
	for _, t := range targets {
		in := Inbound{Project: t.agent.Project, Agent: t.agent.Agent, Account: string(m.Author.ID), Text: text}
		if t.answer != "" {
			in.Answer = t.answer
		} else {
			in.Key = string(m.ID)
			if len(targets) > 1 {
				in.Key += ":" + t.agent.Project + "/" + t.agent.Agent
			}
		}
		id, err := b.hub.Deliver(ctx, in)
		if err != nil {
			b.say(ctx, m, "⚠️ Not delivered to **"+t.agent.Agent+"**"+offlineNote(err)+".")
			continue
		}
		if id != "" {
			_ = b.state.AddPost(ctx, Post{Message: m.ID, Channel: m.ChannelID, Guild: m.GuildID, Project: t.agent.Project, Agent: t.agent.Agent, Egzo: id, Kind: "said"})
		}
	}
}

func offlineNote(err error) string {
	var nd ErrNotDelivered
	if errors.As(err, &nd) && nd.Offline {
		return ": its project is offline"
	}
	return ""
}

// stripRoles takes the role mentions out of a text: the agent reads what the person said to it, not its own address.
func stripRoles(text string, roles []discord.Snowflake) string {
	for _, r := range roles {
		text = strings.ReplaceAll(text, "<@&"+string(r)+">", "")
	}
	return text
}

// known reports whether a person's account is linked. One the bridge does not know is asked of the hub once, at most every minute per
// person: the account may have been linked a moment ago.
func (b *Bridge) known(ctx context.Context, user discord.Snowflake) bool {
	if b.linked(user) {
		return true
	}
	b.mu.Lock()
	last := b.asked[user]
	if time.Since(last) < time.Minute {
		b.mu.Unlock()
		return false
	}
	b.asked[user] = time.Now()
	b.mu.Unlock()
	if err := b.refreshAccounts(ctx); err != nil {
		return false
	}
	return b.linked(user)
}

// hint tells a person who is not linked how to be, at most once in ten minutes.
func (b *Bridge) hint(ctx context.Context, m discord.Message) {
	b.mu.Lock()
	last := b.hinted[m.Author.ID]
	if time.Since(last) < 10*time.Minute {
		b.mu.Unlock()
		return
	}
	b.hinted[m.Author.ID] = time.Now()
	b.mu.Unlock()
	b.say(ctx, m, "To talk to agents, link your Discord account to your egzo hub user first: run `/egzo register`.")
}

// say replies to a message with plain text that pings nobody.
func (b *Bridge) say(ctx context.Context, m discord.Message, text string) {
	no := false
	_, err := b.rest.CreateMessage(ctx, m.ChannelID, discord.MessageCreate{Content: text, AllowedMentions: &discord.AllowedMentions{Parse: []string{}},
		MessageReference: &discord.MessageReference{MessageID: m.ID, FailIfNotExists: &no}})
	if err != nil {
		b.log.Warn("reply on discord", "err", err)
	}
}

// --- what agents say -------------------------------------------------------------------------------------------------------------

// outbound follows the hub's outbox, from where the bridge was, for as long as ctx lasts.
func (b *Bridge) outbound(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		after, err := b.state.Cursor(ctx)
		if err != nil {
			b.log.Error("read the cursor", "err", err)
		}
		began := time.Now()
		err = b.hub.Stream(ctx, after, func(e Event) error {
			b.outgoing(ctx, e)
			return b.state.SetCursor(ctx, e.Seq)
		})
		if ctx.Err() != nil {
			return
		}
		b.log.Warn("the hub's stream ended, reading it again", "err", err)
		if time.Since(began) > time.Minute {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

// outgoing is one event of the outbox.
func (b *Bridge) outgoing(ctx context.Context, e Event) {
	switch e.Type {
	case "message":
		if strings.HasPrefix(e.To, "user:") {
			b.post(ctx, e)
		}
	case "agent.removed":
		b.dropAgents(ctx, e.Project, e.Agent, "the agent was removed")
	case "project.removed":
		b.dropAgents(ctx, e.Project, "", "its project left the hub")
	case "account.linked", "account.unlinked":
		if err := b.refreshAccounts(ctx); err != nil {
			b.log.Warn("read the linked accounts", "err", err)
		}
	}
}

// dropAgents deletes the roles of agents that are gone, everywhere.
func (b *Bridge) dropAgents(ctx context.Context, project, agent, why string) {
	list, err := b.state.AgentEverywhere(ctx, project, agent)
	if err != nil {
		return
	}
	for _, a := range list {
		if err := b.rest.DeleteRole(ctx, a.Guild, a.Role, "egzo: "+why); err != nil {
			b.log.Warn("delete an agent's role", "agent", a.Agent, "err", err)
		}
		_ = b.state.RemoveAgent(ctx, a.Guild, a.Role)
	}
}

// post shows what an agent says to a person: as a reply where the person spoke to it, or in the agent's home channel.
func (b *Bridge) post(ctx context.Context, e Event) {
	b.mu.Lock()
	account, ok := b.byUser[strings.TrimPrefix(e.To, "user:")]
	b.mu.Unlock()
	if !ok {
		return // not a person on Discord
	}
	var channel, guild discord.Snowflake
	var reference *discord.MessageReference
	if e.Re != "" {
		if p, found, _ := b.state.PostFor(ctx, e.Re); found {
			no := false
			channel, guild, reference = p.Channel, p.Guild, &discord.MessageReference{MessageID: p.Message, FailIfNotExists: &no}
		}
	}
	if channel == "" {
		list, _ := b.state.AgentEverywhere(ctx, e.Project, e.Agent)
		if len(list) == 0 {
			b.log.Info("an agent spoke to a person, and is in no channel", "agent", e.Agent, "to", e.To)
			return
		}
		channel, guild = list[0].Home, list[0].Guild
	}
	for i, msg := range render(e, discord.Snowflake(account.Account)) {
		if i == 0 {
			msg.MessageReference = reference
		}
		sent, err := b.rest.CreateMessage(ctx, channel, msg)
		if err != nil {
			b.log.Warn("post what an agent said", "agent", e.Agent, "err", err)
			return
		}
		_ = b.state.AddPost(ctx, Post{Message: sent.ID, Channel: channel, Guild: guild, Project: e.Project, Agent: e.Agent, Egzo: e.ID, Kind: e.Kind, Choices: e.Choices})
	}
}

// --- commands and buttons --------------------------------------------------------------------------------------------------------

func commands() []discord.Command {
	agentOption := func(description string) map[string]any {
		return map[string]any{"type": 3, "name": "agent", "description": description, "required": true, "autocomplete": true}
	}
	options, _ := json.Marshal([]any{
		map[string]any{"type": 1, "name": "register", "description": "Link your Discord account to your egzo hub user"},
		map[string]any{"type": 2, "name": "agent", "description": "The agents of this channel", "options": []any{
			map[string]any{"type": 1, "name": "add", "description": "Add an agent to this channel, as a role to mention", "options": []any{
				agentOption("The agent"),
				map[string]any{"type": 3, "name": "name", "description": "The role's name, the agent's by default", "max_length": 90},
			}},
			map[string]any{"type": 1, "name": "remove", "description": "Take an agent out of this channel", "options": []any{
				agentOption("The agent"),
				map[string]any{"type": 5, "name": "everywhere", "description": "Out of every channel of this server, and its role deleted"},
			}},
			map[string]any{"type": 1, "name": "list", "description": "The agents of this channel"},
		}},
		map[string]any{"type": 1, "name": "say", "description": "Say something to an agent", "options": []any{
			agentOption("The agent"),
			map[string]any{"type": 3, "name": "text", "description": "What to say", "required": true, "max_length": 4000},
			map[string]any{"type": 5, "name": "interrupt", "description": "Interrupt what it is doing"},
		}},
	})
	return []discord.Command{{Name: "egzo", Description: "Talk to egzo agents", Options: options, Contexts: []int{0}}}
}

// path is a command's subcommands, and the options of the last.
func path(options []discord.CommandOption) ([]string, map[string]discord.CommandOption) {
	var names []string
	for len(options) == 1 && (options[0].Type == 1 || options[0].Type == 2) {
		names = append(names, options[0].Name)
		options = options[0].Options
	}
	out := map[string]discord.CommandOption{}
	for _, o := range options {
		out[o.Name] = o
	}
	return names, out
}

func str(o discord.CommandOption) string {
	var s string
	_ = json.Unmarshal(o.Value, &s)
	return s
}

func boolean(o discord.CommandOption) bool {
	var v bool
	_ = json.Unmarshal(o.Value, &v)
	return v
}

func (b *Bridge) onInteraction(ctx context.Context, i discord.Interaction) {
	if i.Member == nil || i.Member.User == nil || i.GuildID == "" {
		return
	}
	switch i.Type {
	case discord.InteractionAutocomplete:
		b.autocomplete(ctx, i)
	case discord.InteractionCommand:
		b.command(ctx, i)
	case discord.InteractionComponent:
		b.click(ctx, i)
	}
}

// autocomplete offers the agents: those the person may chat with, or for remove, those in this channel.
func (b *Bridge) autocomplete(ctx context.Context, i discord.Interaction) {
	names, options := path(i.Data.Options)
	typed := strings.ToLower(str(options["agent"]))
	var choices []discord.Choice
	if len(names) == 2 && names[1] == "remove" {
		place, _ := b.place(i.ChannelID)
		list, _ := b.state.AgentsIn(ctx, i.GuildID, place)
		for _, a := range list {
			if strings.Contains(strings.ToLower(a.Agent+" "+a.Name), typed) {
				choices = append(choices, discord.Choice{Name: a.Agent + " (@" + a.Name + ")", Value: a.Project + "/" + a.Agent})
			}
		}
	} else {
		list, err := b.hub.Agents(ctx, string(i.Member.User.ID))
		if errors.Is(err, ErrNotLinked) {
			choices = []discord.Choice{{Name: "Link your account first: /egzo register", Value: "-"}}
		}
		for _, a := range list {
			if len(names) == 2 && names[1] == "add" && !a.Admin {
				continue
			}
			label := a.ProjectName + "/" + a.Agent
			if strings.Contains(strings.ToLower(label), typed) {
				choices = append(choices, discord.Choice{Name: truncate(label+" · "+a.State, 100), Value: a.Project + "/" + a.Agent})
			}
		}
	}
	if len(choices) > 25 {
		choices = choices[:25]
	}
	if choices == nil {
		choices = []discord.Choice{}
	}
	if err := b.rest.Respond(ctx, i, discord.InteractionResponse{Type: discord.ResponseAutocomplete, Data: map[string]any{"choices": choices}}); err != nil {
		b.log.Warn("answer an autocomplete", "err", err)
	}
}

// defer answers an interaction at once, as "thinking": the hub may take longer than the 3 seconds Discord allows. Private answers are
// seen by the person only.
func (b *Bridge) deferAnswer(ctx context.Context, i discord.Interaction, private bool) bool {
	data := map[string]any{}
	if private {
		data["flags"] = discord.Ephemeral
	}
	if err := b.rest.Respond(ctx, i, discord.InteractionResponse{Type: discord.ResponseDeferredMessage, Data: data}); err != nil {
		b.log.Warn("answer an interaction", "err", err)
		return false
	}
	return true
}

// finish replaces the "thinking" answer.
func (b *Bridge) finish(ctx context.Context, i discord.Interaction, text string) (discord.Message, error) {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()
	m, err := b.rest.EditOriginal(ctx, app, i.Token, discord.MessageCreate{Content: text, AllowedMentions: &discord.AllowedMentions{Parse: []string{}}})
	if err != nil {
		b.log.Warn("finish an interaction", "err", err)
	}
	return m, err
}

// splitAgent reads an agent option: project/agent.
func splitAgent(value string) (string, string, bool) {
	project, agent, ok := strings.Cut(value, "/")
	return project, agent, ok && project != "" && agent != ""
}

func (b *Bridge) command(ctx context.Context, i discord.Interaction) {
	names, options := path(i.Data.Options)
	user := i.Member.User
	sub := strings.Join(names, " ")
	public := sub == "say"
	if !b.deferAnswer(ctx, i, !public) {
		return
	}
	switch sub {
	case "register":
		_, url, expires, err := b.hub.Code(ctx, string(user.ID), user.Username)
		if err != nil {
			b.finish(ctx, i, "⚠️ The hub cannot make a code now. Try again in a moment.")
			return
		}
		b.finish(ctx, i, fmt.Sprintf("Open this link, signed in to the egzo hub, to link your Discord account to your hub user. It is good until <t:%d:t>, and only for you:\n%s", expires.Unix(), url))
	case "agent add":
		b.addAgent(ctx, i, str(options["agent"]), strings.TrimSpace(str(options["name"])))
	case "agent remove":
		b.removeAgent(ctx, i, str(options["agent"]), boolean(options["everywhere"]))
	case "agent list":
		place, _ := b.place(i.ChannelID)
		list, _ := b.state.AgentsIn(ctx, i.GuildID, place)
		if len(list) == 0 {
			b.finish(ctx, i, "No agent is in this channel. Add one with `/egzo agent add`.")
			return
		}
		var lines []string
		for _, a := range list {
			lines = append(lines, fmt.Sprintf("<@&%s> is **%s**", a.Role, a.Agent))
		}
		b.finish(ctx, i, "In this channel:\n"+strings.Join(lines, "\n"))
	case "say":
		project, agent, ok := splitAgent(str(options["agent"]))
		if !ok {
			b.finish(ctx, i, "Pick an agent from the list.")
			return
		}
		text := str(options["text"])
		id, err := b.hub.Deliver(ctx, Inbound{Project: project, Agent: agent, Account: string(user.ID), Text: text, Key: string(i.ID), Interrupt: boolean(options["interrupt"])})
		if err != nil {
			b.finish(ctx, i, "⚠️ Not delivered to **"+agent+"**"+offlineNote(err)+".")
			return
		}
		m, err := b.finish(ctx, i, fmt.Sprintf("💬 <@%s> to **%s**: %s", user.ID, agent, text))
		if err == nil && m.ID != "" { // what the agent answers comes back as a reply to this
			_ = b.state.AddPost(ctx, Post{Message: m.ID, Channel: i.ChannelID, Guild: i.GuildID, Project: project, Agent: agent, Egzo: id, Kind: "said"})
		}
	default:
		b.finish(ctx, i, "Unknown command.")
	}
}

// addAgent adds an agent to the channel: Discord's Manage Roles, and admin on the agent's project on the hub.
func (b *Bridge) addAgent(ctx context.Context, i discord.Interaction, value, name string) {
	project, agent, ok := splitAgent(value)
	if !ok {
		b.finish(ctx, i, "Pick an agent from the list.")
		return
	}
	if !i.Member.Has(discord.PermissionManageRoles) {
		b.finish(ctx, i, "Adding an agent takes Discord's Manage Roles permission.")
		return
	}
	allowed, err := b.hub.MayAdd(ctx, string(i.Member.User.ID), project, agent)
	if err != nil {
		b.finish(ctx, i, "⚠️ The hub cannot say now. Try again in a moment.")
		return
	}
	if !allowed {
		b.finish(ctx, i, "You may not add this agent: it takes admin on its project, with a linked account (`/egzo register`).")
		return
	}
	place, _ := b.place(i.ChannelID)
	a, exists, err := b.state.AgentInGuild(ctx, i.GuildID, project, agent)
	if err != nil {
		b.finish(ctx, i, "⚠️ Something went wrong.")
		return
	}
	if !exists {
		if name == "" {
			name = agent
		}
		if b.roleNamed(i.GuildID, name) {
			b.finish(ctx, i, fmt.Sprintf("A role named %q exists already. Choose another with `name:`.", name))
			return
		}
		role, err := b.rest.CreateRole(ctx, i.GuildID, name, "egzo agent add by "+i.Member.User.Username)
		if err != nil {
			b.finish(ctx, i, "⚠️ Discord refused to make the role: the bot needs Manage Roles.")
			return
		}
		a = Agent{Guild: i.GuildID, Role: role.ID, Project: project, Agent: agent, Name: name, Home: place}
		if err := b.state.AddAgent(ctx, a); err != nil {
			b.finish(ctx, i, "⚠️ Something went wrong.")
			return
		}
	}
	if err := b.state.AddPlace(ctx, i.GuildID, a.Role, place); err != nil {
		b.finish(ctx, i, "⚠️ Something went wrong.")
		return
	}
	b.finish(ctx, i, fmt.Sprintf("**%s** is in this channel: mention <@&%s> to talk to it, or reply to what it posts.", agent, a.Role))
}

func (b *Bridge) roleNamed(guild discord.Snowflake, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.roles[guild] {
		if strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

func (b *Bridge) removeAgent(ctx context.Context, i discord.Interaction, value string, everywhere bool) {
	project, agent, ok := splitAgent(value)
	if !ok {
		b.finish(ctx, i, "Pick an agent from the list.")
		return
	}
	if !i.Member.Has(discord.PermissionManageRoles) {
		b.finish(ctx, i, "Removing an agent takes Discord's Manage Roles permission.")
		return
	}
	a, exists, _ := b.state.AgentInGuild(ctx, i.GuildID, project, agent)
	if !exists {
		b.finish(ctx, i, "That agent is in no channel of this server.")
		return
	}
	left := 0
	if !everywhere {
		place, _ := b.place(i.ChannelID)
		left, _ = b.state.RemovePlace(ctx, i.GuildID, a.Role, place)
	}
	if everywhere || left == 0 {
		if err := b.rest.DeleteRole(ctx, i.GuildID, a.Role, "egzo agent remove by "+i.Member.User.Username); err != nil {
			b.log.Warn("delete an agent's role", "err", err)
		}
		_ = b.state.RemoveAgent(ctx, i.GuildID, a.Role)
		b.finish(ctx, i, fmt.Sprintf("**%s** left this server, and its role is deleted.", agent))
		return
	}
	b.finish(ctx, i, fmt.Sprintf("**%s** left this channel. It is still in %d other.", agent, left))
}

// click is a button: a choice of the question the message is.
func (b *Bridge) click(ctx context.Context, i discord.Interaction) {
	n, ok := strings.CutPrefix(i.Data.CustomID, "egzo:c:")
	if !ok || i.Message == nil {
		return
	}
	index, err := strconv.Atoi(n)
	if !b.deferAnswer(ctx, i, true) {
		return
	}
	p, found, _ := b.state.PostOf(ctx, i.Message.ID)
	if err != nil || !found || p.Kind != "question" || index < 0 || index >= len(p.Choices) {
		b.finish(ctx, i, "This question is not open any more.")
		return
	}
	if !b.known(ctx, i.Member.User.ID) {
		b.finish(ctx, i, "Link your Discord account to the hub first: `/egzo register`.")
		return
	}
	choice := p.Choices[index]
	if _, err := b.hub.Deliver(ctx, Inbound{Project: p.Project, Agent: p.Agent, Account: string(i.Member.User.ID), Text: choice, Answer: p.Egzo}); err != nil {
		b.finish(ctx, i, "⚠️ Not delivered to **"+p.Agent+"**"+offlineNote(err)+". It may have been answered already.")
		return
	}
	b.finish(ctx, i, fmt.Sprintf("You answered **%s**: %s", p.Agent, choice))
}
