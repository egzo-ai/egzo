// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// egzo-discord is the hub's Discord bridge (docs/hub-bridges.md): a service of the hub's compose stack, beside the hub, that reaches it
// on its bridge API and Discord on its gateway. Its settings come from the environment:
//
//	EGZO_DISCORD_TOKEN   the bot's token (required)
//	EGZO_BRIDGE_SECRET   the bridge's secret, as the hub's EGZO_BRIDGES names it: discord:<secret> (required)
//	EGZO_HUB_URL         where the hub is, http://hub:8080 by default
//	EGZO_BRIDGE_DATA     where the bridge keeps its state, /data by default (a volume)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/egzo-ai/egzo/hub/internal/discordbridge"
	"github.com/egzo-ai/egzo/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("egzo-discord", version.Version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("egzo-discord stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	token, secret := os.Getenv("EGZO_DISCORD_TOKEN"), os.Getenv("EGZO_BRIDGE_SECRET")
	var missing []string
	if token == "" {
		missing = append(missing, "EGZO_DISCORD_TOKEN (the bot's token, from the Discord Developer Portal)")
	}
	if secret == "" {
		missing = append(missing, "EGZO_BRIDGE_SECRET (as the hub's EGZO_BRIDGES names it: discord:<secret>)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing settings: %v", missing)
	}
	hubURL := os.Getenv("EGZO_HUB_URL")
	if hubURL == "" {
		hubURL = "http://hub:8080"
	}
	dir := os.Getenv("EGZO_BRIDGE_DATA")
	if dir == "" {
		dir = "/data"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	state, err := discordbridge.OpenState(filepath.Join(dir, "discord.db"))
	if err != nil {
		return err
	}
	defer state.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("egzo-discord starting", "hub", hubURL, "version", version.Version)
	err = discordbridge.New(discordbridge.Config{Token: token, Hub: &discordbridge.HubAPI{URL: hubURL, Secret: secret}, State: state, Log: log}).Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
