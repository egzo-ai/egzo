// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package discord

import (
	"context"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/discord/discordtest"
)

func TestTheClientCuttingItsOwnConnectionResumes(t *testing.T) {
	fake := discordtest.New("t")
	t.Cleanup(fake.Close)
	g := &Gateway{Token: "t", Intents: IntentGuilds, URL: fake.GatewayURL()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	connected := fake.Connected()
	go func() { done <- g.Run(ctx) }()
	defer func() { cancel(); <-done }()
	<-connected
	connected = fake.Connected()
	g.drop()
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never came back")
	}
	if id, res := fake.Counts(); id != 1 || res != 1 {
		t.Errorf("identified %d, resumed %d", id, res)
	}
}
