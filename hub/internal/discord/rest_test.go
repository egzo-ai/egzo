// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/discord"
	"github.com/egzo-ai/egzo/hub/internal/discord/discordtest"
)

func rest(t *testing.T) (*discordtest.Server, *discord.REST) {
	t.Helper()
	fake := discordtest.New(token)
	t.Cleanup(fake.Close)
	return fake, discord.NewREST(token, fake.API(), nil)
}

func TestAMessageGoesWithTheTokenAndPingsNobody(t *testing.T) {
	fake, c := rest(t)
	sent, err := c.CreateMessage(context.Background(), "20", discord.MessageCreate{
		Components: []byte(`[{"type":10,"content":"staging or production?"}]`), Flags: discord.MessageFlagComponentsV2,
		AllowedMentions: &discord.AllowedMentions{Parse: []string{}},
	})
	if err != nil || sent.ID == "" || sent.ChannelID != "20" {
		t.Fatalf("sent %+v, %v", sent, err)
	}
	got := fake.Requests("POST /channels/20/messages")
	if len(got) != 1 || got[0].Header.Get("Authorization") != "Bot "+token {
		t.Fatalf("requests %+v", got)
	}
	var body map[string]any
	_ = got[0].JSON(&body)
	if mentions, ok := body["allowed_mentions"].(map[string]any); !ok || len(mentions["parse"].([]any)) != 0 {
		t.Errorf("allowed_mentions = %v", body["allowed_mentions"])
	}
	if body["flags"].(float64) != discord.MessageFlagComponentsV2 {
		t.Errorf("flags = %v", body["flags"])
	}
}

func TestA429IsWaitedOutAndTriedAgain(t *testing.T) {
	fake, c := rest(t)
	fake.RateLimit("/channels/20/messages", 2)
	began := time.Now()
	if _, err := c.CreateMessage(context.Background(), "20", discord.MessageCreate{Content: "x", AllowedMentions: &discord.AllowedMentions{Parse: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Requests("POST /channels/20/messages")); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
	if took := time.Since(began); took < 100*time.Millisecond {
		t.Errorf("it did not wait: %v", took)
	}
	fake.RateLimit("/channels/20/messages", 10)
	var e *discord.Error
	if _, err := c.CreateMessage(context.Background(), "20", discord.MessageCreate{Content: "x"}); !errors.As(err, &e) || e.Status != 429 {
		t.Errorf("after the retries: %v", err)
	}
}

func TestALongTextGoesAsAFile(t *testing.T) {
	fake, c := rest(t)
	_, err := c.CreateMessage(context.Background(), "20", discord.MessageCreate{Content: "the rest is attached",
		AllowedMentions: &discord.AllowedMentions{Parse: []string{}}, Files: []discord.File{{Name: "answer.md", Data: []byte("# long")}}})
	if err != nil {
		t.Fatal(err)
	}
	got := fake.Requests("POST /channels/20/messages")
	if len(got) != 1 || !strings.HasPrefix(got[0].Header.Get("Content-Type"), "multipart/form-data") || !strings.Contains(string(got[0].Body), `filename="answer.md"`) ||
		!strings.Contains(string(got[0].Body), `"attachments":[{"id":0,"filename":"answer.md"}]`) {
		t.Errorf("request %s %q", got[0].Header.Get("Content-Type"), got[0].Body)
	}
}

func TestAnInteractionIsAnsweredWithItsOwnTokenNotTheBots(t *testing.T) {
	fake, c := rest(t)
	err := c.Respond(context.Background(), discord.Interaction{ID: "55", Token: "interaction-token"}, discord.InteractionResponse{Type: discord.ResponseDeferredUpdate})
	if err != nil {
		t.Fatal(err)
	}
	got := fake.Requests("POST /interactions/55/interaction-token/callback")
	if len(got) != 1 || got[0].Header.Get("Authorization") != "" {
		t.Errorf("requests %+v", got)
	}
}

func TestARoleIsMadeMentionableWithNoPermissionsAndAReason(t *testing.T) {
	fake, c := rest(t)
	role, err := c.CreateRole(context.Background(), "10", "coder-1", "egzo agent add by joe")
	if err != nil || role.ID == "" {
		t.Fatalf("%+v %v", role, err)
	}
	got := fake.Requests("POST /guilds/10/roles")
	var body map[string]any
	_ = got[0].JSON(&body)
	if body["mentionable"] != true || body["permissions"] != "0" || got[0].Header.Get("X-Audit-Log-Reason") == "" {
		t.Errorf("body %v reason %q", body, got[0].Header.Get("X-Audit-Log-Reason"))
	}
}

func TestAWrongTokenIsAnError(t *testing.T) {
	fake := discordtest.New(token)
	t.Cleanup(fake.Close)
	c := discord.NewREST("wrong", fake.API(), nil)
	var e *discord.Error
	if _, err := c.Channel(context.Background(), "20"); !errors.As(err, &e) || e.Status != 401 {
		t.Fatalf("err = %v", err)
	}
}
