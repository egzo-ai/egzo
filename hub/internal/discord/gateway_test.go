// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/discord"
	"github.com/egzo-ai/egzo/hub/internal/discord/discordtest"
)

const token = "bot-token"

// heard is what the client's handler got.
type heard struct {
	mu     sync.Mutex
	events []discord.Event
	more   chan struct{}
}

func (h *heard) handle(e discord.Event) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	select {
	case h.more <- struct{}{}:
	default:
	}
}

// contents are the texts of the MESSAGE_CREATE events heard, in order.
func (h *heard) contents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		if e.Type == "MESSAGE_CREATE" {
			var m discord.Message
			_ = json.Unmarshal(e.Data, &m)
			out = append(out, m.Content)
		}
	}
	return out
}

func (h *heard) waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !check() {
		select {
		case <-h.more:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("waited for %s", what)
		}
	}
}

func start(t *testing.T) (*discordtest.Server, *discord.Gateway, *heard, <-chan error) {
	t.Helper()
	fake := discordtest.New(token)
	fake.Heartbeat = 100 * time.Millisecond
	t.Cleanup(fake.Close)
	fake.OnReady(discord.Guild{ID: "10", Name: "team", Channels: []discord.Channel{{ID: "20", Name: "deploys"}}})
	h := &heard{more: make(chan struct{}, 1)}
	rest := discord.NewREST(token, fake.API(), nil)
	gw, err := rest.GatewayBot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := &discord.Gateway{Token: token, Intents: discord.IntentGuilds | discord.IntentGuildMessages | discord.IntentMessageContent, URL: gw.URL, Handler: h.handle}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	connected := fake.Connected()
	go func() { done <- g.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select { // a test that read the result already leaves nothing here
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("never connected")
	}
	return fake, g, h, done
}

func message(text string) discord.Message {
	return discord.Message{ID: "99", ChannelID: "20", GuildID: "10", Author: discord.User{ID: "7", Username: "joe"}, Content: text}
}

func TestTheClientIdentifiesAndHearsTheGuildsAndMessages(t *testing.T) {
	fake, _, h, _ := start(t)
	fake.Dispatch("MESSAGE_CREATE", message("hello"))
	h.waitFor(t, "the message", func() bool { return len(h.contents()) == 1 })
	h.mu.Lock()
	types := []string{h.events[0].Type, h.events[1].Type}
	h.mu.Unlock()
	if types[0] != "READY" || types[1] != "GUILD_CREATE" {
		t.Errorf("events began with %v", types)
	}
	if id, res := fake.Counts(); id != 1 || res != 0 {
		t.Errorf("identified %d times, resumed %d", id, res)
	}
}

func TestADroppedConnectionResumesAndWhatWasSaidMeanwhileArrivesOnceInOrder(t *testing.T) {
	fake, g, h, _ := start(t)
	fake.Dispatch("MESSAGE_CREATE", message("one"))
	h.waitFor(t, "the first", func() bool { return len(h.contents()) == 1 })
	connected := fake.Connected()
	fake.Drop()
	fake.Dispatch("MESSAGE_CREATE", message("two")) // while it is away
	fake.Dispatch("MESSAGE_CREATE", message("three"))
	<-connected
	fake.Dispatch("MESSAGE_CREATE", message("four"))
	h.waitFor(t, "all four", func() bool { return len(h.contents()) == 4 })
	if got := h.contents(); got[0] != "one" || got[1] != "two" || got[2] != "three" || got[3] != "four" {
		t.Errorf("heard %v", got)
	}
	if id, res := fake.Counts(); id != 1 || res != 1 {
		t.Errorf("identified %d, resumed %d: want one of each", id, res)
	}
	if s := g.Stats(); s.Resumes != 1 {
		t.Errorf("stats %+v", s)
	}
}

func TestAZombieConnectionIsNoticedByItsMissingAckAndResumed(t *testing.T) {
	fake, g, h, _ := start(t)
	connected := fake.Connected()
	fake.Zombie()
	fake.Dispatch("MESSAGE_CREATE", message("said to a zombie"))
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("the zombie was never noticed")
	}
	h.waitFor(t, "what was said", func() bool { return len(h.contents()) == 1 })
	if s := g.Stats(); s.Zombies != 1 || s.Resumes != 1 {
		t.Errorf("stats %+v", s)
	}
}

func TestDiscordAskingToReconnectResumes(t *testing.T) {
	fake, _, h, _ := start(t)
	connected := fake.Connected()
	fake.Reconnect()
	<-connected
	fake.Dispatch("MESSAGE_CREATE", message("after"))
	h.waitFor(t, "the message", func() bool { return len(h.contents()) == 1 })
	if id, res := fake.Counts(); id != 1 || res != 1 {
		t.Errorf("identified %d, resumed %d", id, res)
	}
}

func TestASessionThatCannotBeResumedIsIdentifiedAgain(t *testing.T) {
	fake, _, _, _ := start(t)
	connected := fake.Connected()
	fake.InvalidSession(false)
	select {
	case <-connected:
	case <-time.After(10 * time.Second): // Discord asks for 1 to 5 seconds first
		t.Fatal("never came back")
	}
	if id, res := fake.Counts(); id != 2 || res != 0 {
		t.Errorf("identified %d, resumed %d: want a second identify", id, res)
	}
}

func TestAWrongTokenStopsTheClientWithAReason(t *testing.T) {
	fake := discordtest.New(token)
	t.Cleanup(fake.Close)
	g := &discord.Gateway{Token: "wrong", Intents: discord.IntentGuilds, URL: fake.GatewayURL()}
	done := make(chan error, 1)
	go func() { done <- g.Run(context.Background()) }()
	select {
	case err := <-done:
		var f *discord.FatalError
		if !errors.As(err, &f) || f.Code != 4004 {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("it kept trying with a wrong token")
	}
}

func TestIntentsTheBotMayNotHaveStopTheClientAndSayWhatToEnable(t *testing.T) {
	fake, _, _, done := start(t)
	fake.CloseWith(4014, "Disallowed intent(s).")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Message Content") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("it kept trying")
	}
}

func TestNoSessionStartsLeftWaitsForTheResetBeforeIdentifying(t *testing.T) {
	fake := discordtest.New(token)
	t.Cleanup(fake.Close)
	fake.SessionStarts(0, 400*time.Millisecond)
	rest := discord.NewREST(token, fake.API(), nil)
	g := &discord.Gateway{Token: token, Intents: discord.IntentGuilds, URL: fake.GatewayURL(), Sessions: rest.GatewayBot}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	connected := fake.Connected()
	began := time.Now()
	go func() { done <- g.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("never connected")
	}
	if took := time.Since(began); took < 400*time.Millisecond {
		t.Errorf("identified after %v, before the starts renewed", took)
	}
}
