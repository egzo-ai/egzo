// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build discordlive

// The client against the real Discord, with a test bot in a private server. Never in CI:
//
//	DISCORD_TOKEN_FILE=~/.discord-test-token DISCORD_LIVE_LOG=/tmp/live.log go test -tags discordlive -run Live -v -timeout 30m ./internal/discord/
//
// The first part needs nobody. Then a person in the server mentions the test role, replies to the bot, clicks a button and runs the
// command, with its autocomplete; the test waits up to 20 minutes for that, and says what it saw.
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLive(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("DISCORD_TOKEN_FILE"))
	if err != nil {
		t.Skip("no DISCORD_TOKEN_FILE")
	}
	token := strings.TrimSpace(string(data))
	logFile, _ := os.OpenFile(os.Getenv("DISCORD_LIVE_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	say := func(format string, args ...any) {
		line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
		t.Log(line)
		if logFile != nil {
			fmt.Fprintln(logFile, line)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	rest := NewREST(token, DefaultURL, nil)
	app, err := rest.Application(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := rest.GatewayBot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	say("application %s; gateway %s; sessions left today %d of %d", app.Name, gw.URL, gw.SessionStartLimit.Remaining, gw.SessionStartLimit.Total)

	var mu sync.Mutex
	var guild Guild
	var role Role
	var question Message
	seen := map[string]string{}
	events := make(chan Event, 64)
	g := &Gateway{Token: token, Intents: IntentGuilds | IntentGuildMessages | IntentMessageContent, URL: gw.URL, Handler: func(e Event) { events <- e }}
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	note := func(what, how string) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[what]; !ok {
			seen[what] = how
			say("SEEN %s: %s", what, how)
		}
	}
	wait := func(what string, check func(Event) bool, limit time.Duration) Event {
		deadline := time.After(limit)
		for {
			select {
			case e := <-events:
				handle(t, ctx, rest, app, e, &mu, &role, &question, note, say)
				if check(e) {
					return e
				}
			case err := <-done:
				t.Fatalf("the gateway stopped: %v", err)
			case <-deadline:
				t.Fatalf("waited %v for %s", limit, what)
			}
		}
	}

	wait("READY", func(e Event) bool { return e.Type == "READY" }, 30*time.Second)
	say("READY")
	e := wait("GUILD_CREATE", func(e Event) bool { return e.Type == "GUILD_CREATE" }, 30*time.Second)
	_ = json.Unmarshal(e.Data, &guild)
	channel := pickChannel(guild)
	say("guild %q with %d channels, %d roles; testing in #%s", guild.Name, len(guild.Channels), len(guild.Roles), channel.Name)

	g.drop()
	wait("RESUMED", func(e Event) bool { return e.Type == "RESUMED" }, 60*time.Second)
	say("dropped the connection and RESUMED; stats %+v", g.Stats())

	r, err := rest.CreateRole(ctx, guild.ID, "egzo-test-agent", "egzo live test")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	role = r
	mu.Unlock()
	defer func() {
		if err := rest.DeleteRole(context.Background(), guild.ID, r.ID, "egzo live test done"); err != nil {
			say("delete role: %v", err)
		}
		_ = rest.SetGuildCommands(context.Background(), app.ID, guild.ID, []Command{})
		say("cleaned up the role and the command")
	}()
	say("role @%s made (%s)", r.Name, r.ID)

	options, _ := json.Marshal([]map[string]any{{"type": 3, "name": "agent", "description": "an agent", "required": true, "autocomplete": true}})
	if err := rest.SetGuildCommands(ctx, app.ID, guild.ID, []Command{{Name: "egzo-test", Description: "egzo live test", Options: options}}); err != nil {
		t.Fatal(err)
	}
	say("command /egzo-test registered in the guild")

	components := json.RawMessage(`[{"type":17,"accent_color":5793266,"components":[
		{"type":10,"content":"**egzo live test.** Deploy to staging or production?\n@everyone this line must ping nobody."},
		{"type":1,"components":[
			{"type":2,"style":1,"label":"staging","custom_id":"egzo:q1:staging"},
			{"type":2,"style":4,"label":"production","custom_id":"egzo:q1:production"}]},
		{"type":13,"file":{"url":"attachment://answer.md"}}]}]`)
	var sent map[string]any
	body, err := multipartMessage(MessageCreate{Components: components, Flags: MessageFlagComponentsV2, AllowedMentions: &AllowedMentions{Parse: []string{}},
		Files: []File{{Name: "answer.md", Data: []byte("# A long answer\n\nWhat did not fit in the message.\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rest.Do(ctx, "POST", "/channels/"+string(channel.ID)+"/messages", body, &sent); err != nil {
		t.Fatalf("components v2 message: %v", err)
	}
	mu.Lock()
	question = Message{ID: Snowflake(fmt.Sprint(sent["id"])), ChannelID: channel.ID}
	mu.Unlock()
	say("posted a Components V2 message %v: mention_everyone=%v, attachments=%d", sent["id"], sent["mention_everyone"], len(asList(sent["attachments"])))

	say("NOW, in #%s: (1) post a message that mentions @egzo-test-agent, (2) reply to the bot's message, (3) click a button, (4) run /egzo-test and pick an agent from the autocomplete", channel.Name)
	want := []string{"role mention with content", "reply to the bot", "button click", "autocomplete", "slash command"}
	wait("the person's part", func(Event) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, w := range want {
			if _, ok := seen[w]; !ok {
				return false
			}
		}
		return true
	}, 20*time.Minute)
	say("ALL SEEN; stats %+v", g.Stats())
}

// handle answers what the person does, and notes it.
func handle(t *testing.T, ctx context.Context, rest *REST, app Application, e Event, mu *sync.Mutex, role *Role, question *Message, note func(string, string), say func(string, ...any)) {
	mu.Lock()
	r, q := *role, *question
	mu.Unlock()
	switch e.Type {
	case "MESSAGE_CREATE":
		var m Message
		_ = json.Unmarshal(e.Data, &m)
		if m.Author.Bot {
			return
		}
		say("message from %s: content=%q mention_roles=%v reference=%v", m.Author.Username, m.Content, m.MentionRoles, m.MessageReference)
		for _, id := range m.MentionRoles {
			if r.ID != "" && id == r.ID {
				if m.Content == "" {
					note("role mention WITHOUT content", "the content was empty: Message Content is off")
				} else {
					note("role mention with content", fmt.Sprintf("%q", m.Content))
				}
				_, err := rest.CreateMessage(ctx, m.ChannelID, MessageCreate{Content: "heard you, <@" + string(m.Author.ID) + "> (this must not ping)",
					AllowedMentions: &AllowedMentions{Parse: []string{}}, MessageReference: &MessageReference{MessageID: m.ID}})
				if err != nil {
					say("reply: %v", err)
				}
			}
		}
		if m.MessageReference != nil && q.ID != "" && m.MessageReference.MessageID == q.ID {
			note("reply to the bot", fmt.Sprintf("%q", m.Content))
		}
	case "INTERACTION_CREATE":
		var i Interaction
		_ = json.Unmarshal(e.Data, &i)
		user := ""
		if i.Member != nil && i.Member.User != nil {
			user = i.Member.User.Username
		}
		switch i.Type {
		case InteractionComponent:
			err := rest.Respond(ctx, i, InteractionResponse{Type: ResponseMessage, Data: map[string]any{"content": "you chose " + i.Data.CustomID, "flags": Ephemeral,
				"allowed_mentions": AllowedMentions{Parse: []string{}}}})
			note("button click", fmt.Sprintf("%s by %s, answered: %v", i.Data.CustomID, user, err))
		case InteractionAutocomplete:
			err := rest.Respond(ctx, i, InteractionResponse{Type: ResponseAutocomplete, Data: map[string]any{"choices": []Choice{
				{Name: "web/coder-1", Value: "web/coder-1"}, {Name: "web/reviewer-1", Value: "web/reviewer-1"}}}})
			note("autocomplete", fmt.Sprintf("typing %s, answered: %v", options(i.Data.Options), err))
		case InteractionCommand:
			err := rest.Respond(ctx, i, InteractionResponse{Type: ResponseMessage, Data: map[string]any{"content": "/" + i.Data.Name + " " + options(i.Data.Options),
				"flags": Ephemeral, "allowed_mentions": AllowedMentions{Parse: []string{}}}})
			note("slash command", fmt.Sprintf("/%s %s by %s (permissions %s), answered: %v", i.Data.Name, options(i.Data.Options), user, perms(i), err))
		}
	}
}

func options(list []CommandOption) string {
	var out []string
	for _, o := range list {
		out = append(out, o.Name+"="+string(o.Value))
	}
	return strings.Join(out, " ")
}

func perms(i Interaction) string {
	if i.Member == nil {
		return ""
	}
	return i.Member.Permissions
}

func pickChannel(g Guild) Channel {
	var first Channel
	for _, c := range g.Channels {
		if c.Type != ChannelText {
			continue
		}
		if c.Name == "general" {
			return c
		}
		if first.ID == "" {
			first = c
		}
	}
	return first
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
