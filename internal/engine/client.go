// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
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
	return probe(ctx, api, "\negzo uses the engine DOCKER_HOST points at, like the docker CLI. For rootless Podman: "+
		"systemctl --user enable --now podman.socket; export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock")
}

func (c *Client) Close() error { return c.API.Close() }
