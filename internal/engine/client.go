// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/client"
)

// Client is a connection to a container engine.
type Client struct {
	API  *client.Client
	Host string
	// Podman and Rootless are what IsPodman and IsRootless answer once they have asked the engine; a Client built by hand
	// (a test) sets them and is never asked.
	Podman, Rootless bool
	facts            *facts
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

// APIAtLeast reports whether the engine speaks the Docker API at version v or later. The client negotiates its version
// down to the engine's when it connects, so the version in use is the engine's own when the engine is the older one.
func (c *Client) APIAtLeast(v string) bool {
	return c.API != nil && versions.GreaterThanOrEqualTo(c.API.ClientVersion(), v)
}

// IsNameConflict reports whether creating a container failed because its name is taken. Docker answers
// 409 Conflict; Podman answers a plain 500 whose message says the name is already in use.
func IsNameConflict(err error) bool {
	return err != nil && (cerrdefs.IsConflict(err) || strings.Contains(err.Error(), "is already in use"))
}

// IsNoSuchContainer reports whether an engine call failed because the container is gone. Docker answers
// 404 Not Found; Podman answers a plain 500 for a container that went away while the call was running.
func IsNoSuchContainer(err error) bool {
	return err != nil && (cerrdefs.IsNotFound(err) || strings.Contains(err.Error(), "no such container") || strings.Contains(err.Error(), "no container with"))
}
