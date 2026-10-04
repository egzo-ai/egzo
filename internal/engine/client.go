package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/client"
)

// Client is a connection to a container engine.
type Client struct {
	API  *client.Client
	Host string
}

// Connect opens the engine. DOCKER_HOST wins when set. Otherwise hint (docker, podman or auto,
// from runtime.engine) selects which well-known sockets are tried, in order.
func Connect(ctx context.Context, hint string) (*Client, error) {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return open(ctx, host)
	}
	var tried []string
	for _, host := range candidateSockets(hint) {
		c, err := open(ctx, host)
		if err == nil {
			return c, nil
		}
		tried = append(tried, host)
	}
	return nil, fmt.Errorf("no container engine reachable (tried %v); start Docker or Podman, or set DOCKER_HOST", tried)
}

func open(ctx context.Context, host string) (*Client, error) {
	api, err := client.NewClientWithOpts(client.WithHost(host), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	if _, err := api.Ping(ctx); err != nil {
		api.Close()
		return nil, fmt.Errorf("engine at %s: %w", host, err)
	}
	return &Client{API: api, Host: host}, nil
}

func candidateSockets(hint string) []string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	docker := []string{"unix:///var/run/docker.sock", "unix://" + filepath.Join(runtimeDir, "docker.sock")}
	podman := []string{"unix://" + filepath.Join(runtimeDir, "podman", "podman.sock"), "unix:///run/podman/podman.sock"}
	switch hint {
	case "docker":
		return docker
	case "podman":
		return podman
	}
	return append(docker, podman...)
}

func (c *Client) Close() error { return c.API.Close() }
