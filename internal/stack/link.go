// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/hublink"
)

// Where the link sidecar keeps things, and where it finds the engine.
const (
	LinkEngineSocket = "/run/engine.sock"
	linkState        = "/var/lib/egzo/link"
	linkMemory       = 256 << 20
	linkPids         = 256
	defaultSocket    = "/var/run/docker.sock"
)

// LinkSocket is the engine's socket as the link gets it: its path on the engine's host, and the group that may open it.
type LinkSocket struct {
	Path  string
	Group string
}

// FindLinkSocket learns what the link needs of the engine's socket. Only the engine's host can say: the CLI may be on another
// machine. So a throwaway container mounts the socket and reads its group. A socket that is not at the path fails here, before
// anything is changed.
func FindLinkSocket(ctx context.Context, c *engine.Client, image string) (LinkSocket, error) {
	socket := LinkSocket{Path: socketPath(c)}
	if c.IsRootless() {
		return socket, nil // the invoking user owns the socket, and is root in the container's namespace
	}
	if err := ensureImage(ctx, c, image); err != nil {
		return socket, err
	}
	out, err := RunPrep(ctx, c, PrepSpec{
		Image:    image,
		Cmd:      []string{"/egzo", "link", "socket-group", LinkEngineSocket},
		Mounts:   []MountSpec{{Source: socket.Path, Target: LinkEngineSocket, Bind: true}},
		Identity: engine.Identity{Project: "egzo-system-check", Service: "linkprobe", Kind: "prep"},
		Timeout:  time.Minute,
	})
	if err != nil {
		return socket, fmt.Errorf("the link needs the engine's socket at %s on the engine's host: %w", socket.Path, err)
	}
	socket.Group = strings.TrimSpace(out)
	return socket, nil
}

func socketPath(c *engine.Client) string {
	if host := c.API.DaemonHost(); strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return defaultSocket
}

// linkUser is who the link runs as: the sidecars' unprivileged user, in the group of the socket. Primary, not supplementary: that
// is the one form every runtime honours.
func linkUser(socket LinkSocket) string {
	if socket.Group == "" {
		return "0:0" // a rootless engine: root in the container is the user who owns the engine
	}
	return "65532:" + socket.Group
}

func desireLink(project *config.Resolved, in Inputs, identity engine.Identity) (NetworkSpec, VolumeSpec, ContainerSpec, error) {
	network := NetworkSpec{Name: project.Name + "_link", Identity: identity}
	state := VolumeSpec{Name: project.Name + "_link", Identity: identity}
	env := []string{"EGZO_PROJECT=" + project.Name, "EGZO_HUB=" + project.Hub.URL}
	if project.Hub.CA != "" {
		ca, err := config.ReadUpstreamCA(project.Hub.CA)
		if err != nil {
			return network, state, ContainerSpec{}, fmt.Errorf("hub.ca: %w", err)
		}
		env = append(env, "EGZO_HUB_CA="+string(ca))
	}
	return network, state, ContainerSpec{
		Name:         project.Name + "-link-1",
		Image:        in.Image,
		Cmd:          []string{"/egzo", "link"},
		User:         linkUser(in.LinkSocket),
		OwnedVolumes: []string{state.Name},
		Memory:       linkMemory,
		MemorySwap:   linkMemory,
		PidsLimit:    linkPids,
		Env:          env,
		Mounts: []MountSpec{
			{Source: state.Name, Target: linkState},
			{Source: in.LinkSocket.Path, Target: LinkEngineSocket, Bind: true},
		},
		Tmpfs:          map[string]string{"/tmp": sidecarTmpfs, "/run/egzo": sidecarTmpfsState},
		Network:        network.Name,
		Healthcheck:    []string{"/egzo", "link", "--healthcheck"},
		ReadonlyRootfs: true,
		Identity:       identity,
	}, nil
}

// LinkUnlock is what `up` hands the link through stdin, so it is never in `inspect`.
type LinkUnlock struct {
	SecretKey string
	Proxy     string // a company proxy, which may hold credentials
}

func linkRequest(ctx context.Context, c *engine.Client, project, method, path string, body io.Reader) ([]byte, error) {
	result, err := c.Exec(ctx, project+"-link-1", []string{"/egzo", "link", "request", method, path}, body)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(result.Stderr)))
	}
	return result.Stdout, nil
}

// LinkStatus asks the link where it stands. wait, when not zero, lets a link that has just started get its first answer.
func LinkStatus(ctx context.Context, c *engine.Client, project string, wait time.Duration) (hublink.Status, error) {
	var status hublink.Status
	reply, err := linkRequest(ctx, c, project, "GET", "/status?settle="+wait.String(), nil)
	if err != nil {
		return status, err
	}
	return status, json.Unmarshal(reply, &status)
}

// settleLink is how long `up` waits for the link's first answer. `up` never waits for a person.
const settleLink = 5 * time.Second

// unlockLink gives the link the secret key and says where it stands.
func unlockLink(ctx context.Context, c *engine.Client, project, secretKey string, out, warnings io.Writer) error {
	body, _ := json.Marshal(LinkUnlock{SecretKey: secretKey, Proxy: os.Getenv("HTTPS_PROXY")})
	if _, err := linkRequest(ctx, c, project, "PUT", "/unlock", bytes.NewReader(body)); err != nil {
		return fmt.Errorf("the link: %w", err)
	}
	status, err := LinkStatus(ctx, c, project, settleLink)
	if err != nil {
		return fmt.Errorf("the link: %w", err)
	}
	line := "link: " + status.Text()
	switch status.State {
	case hublink.StateApproved, hublink.StateUnregistered:
		fmt.Fprintln(out, line)
	default:
		fmt.Fprintln(warnings, "warning: "+line) // the project runs without the hub
	}
	return nil
}
