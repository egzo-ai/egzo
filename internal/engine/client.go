// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"
)

// Client is a connection to a container engine.
type Client struct {
	API  *client.Client
	Host string
	// Podman is true when the engine behind the Docker-compatible API is Podman.
	Podman bool
	// Rootless is true when the engine runs without root: it maps users itself, like Podman.
	Rootless bool
}

// Connect opens the engine the environment points at, the way the docker CLI does: DOCKER_HOST,
// with /var/run/docker.sock as the default. Podman users point DOCKER_HOST at their Podman socket.
func Connect(ctx context.Context) (*Client, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	host := api.DaemonHost()
	probe, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := api.Ping(probe); err != nil {
		api.Close()
		return nil, fmt.Errorf("no container engine reachable at %s: %w\n"+
			"egzo uses the engine DOCKER_HOST points at, like the docker CLI. For rootless Podman: "+
			"systemctl --user enable --now podman.socket; export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock", host, err)
	}
	c := &Client{API: api, Host: host}
	if info, err := api.Info(probe); err == nil {
		for _, option := range info.SecurityOptions {
			c.Rootless = c.Rootless || strings.Contains(option, "rootless")
		}
	}
	if version, err := api.ServerVersion(probe); err == nil {
		c.Podman = strings.Contains(strings.ToLower(version.Platform.Name), "podman")
		for _, component := range version.Components {
			c.Podman = c.Podman || strings.Contains(strings.ToLower(component.Name), "podman")
		}
	}
	return c, nil
}

func (c *Client) Close() error { return c.API.Close() }
