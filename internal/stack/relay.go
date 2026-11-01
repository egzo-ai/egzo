// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// maxRelayAttempts is how often a spawn tries again when the engine says a chosen host port is taken.
const maxRelayAttempts = 16

// outsidePorts chooses the host port of each declared port. The start for a name is the instance's id and the name hashed into the pool, so
// the same instance asks for the same port again, and a port in use by another relay, or already chosen here, is skipped for the next one.
func outsidePorts(instanceID string, names []string, low, high int, taken map[int]bool) (map[string]int, error) {
	size := high - low + 1
	chosen := map[string]int{}
	used := map[int]bool{}
	for _, name := range names {
		sum := sha256.Sum256([]byte(instanceID + name))
		start := int(binary.BigEndian.Uint64(sum[:8]) % uint64(size))
		found := false
		for k := range size {
			candidate := low + (start+k)%size
			if taken[candidate] || used[candidate] {
				continue
			}
			chosen[name], used[candidate], found = candidate, true, true
			break
		}
		if !found {
			return nil, fmt.Errorf("no port of the pool %d-%d is free for %q", low, high, name)
		}
	}
	return chosen, nil
}

// portsInUse are the host ports the project's other relays publish, whether they run or not.
func portsInUse(observed Observed, except string) map[int]bool {
	taken := map[int]bool{}
	for _, r := range observed.Resources {
		if r.Type == "container" && r.Kind == kindRelay && r.Name != except {
			for _, port := range r.HostPorts {
				taken[port] = true
			}
		}
	}
	return taken
}

// portConflict says whether the engine refused to start a container because a host port is taken.
func portConflict(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "port is already allocated") || strings.Contains(text, "address already in use")
}

// localAddress is where people reach a declared port from outside: a URL for http, host:port for the rest.
func localAddress(local *config.ResolvedLocalPorts, port config.ResolvedPort, outside int) string {
	hostPort := stdnet.JoinHostPort(local.Host, strconv.Itoa(outside))
	if port.Protocol == config.ProtocolHTTP {
		return "http://" + hostPort + "/"
	}
	return hostPort
}

// startRelay makes and starts the relay of a new instance, on the pinned host ports it chooses, and returns the address of each declared port.
// The host ports are free as far as the project's other relays say; a port something else holds is told by the engine when the relay starts,
// and the relay is made again without it.
func startRelay(ctx context.Context, c *engine.Client, p spawnPlan, tokens map[string]string, undo *undoStack, out io.Writer) (map[string]string, error) {
	local := p.view.LocalPorts
	agent := p.view.Agents[p.name]
	observed, err := Observe(ctx, c, p.project)
	if err != nil {
		return nil, err
	}
	name := RelayContainer(p.project, p.name)
	taken := portsInUse(observed, name)
	names := sortedKeys(agent.Ports)
	if err := ensureImage(ctx, c, p.published.Image); err != nil {
		return nil, err
	}
	var last error
	for range maxRelayAttempts {
		outside, err := outsidePorts(p.identity.ID, names, local.PoolLow, local.PoolHigh, taken)
		if err != nil {
			return nil, err
		}
		spec, err := p.relay(tokens, RelayInput{Outside: outside})
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(out, Action{Verb: "create", Type: "container", Name: spec.Name})
		id, err := createContainer(ctx, c, spec, p.resources.PrepImage)
		if err == nil {
			undo.add(func(ctx context.Context) {
				timeout := stopTimeoutSeconds
				c.API.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
				c.API.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
			})
			addresses := map[string]string{}
			for _, portName := range names {
				addresses[portName] = localAddress(local, agent.Ports[portName], outside[portName])
			}
			return addresses, nil
		}
		if id != "" {
			c.API.ContainerRemove(context.WithoutCancel(ctx), id, container.RemoveOptions{Force: true})
		}
		if !portConflict(err) {
			return nil, fmt.Errorf("create container %s: %w", spec.Name, err)
		}
		last = err
		// the engine names the port it could not take; when it does not, none of the chosen ones is trusted
		marked := false
		for _, port := range outside {
			if strings.Contains(err.Error(), ":"+strconv.Itoa(port)) {
				taken[port], marked = true, true
			}
		}
		if !marked {
			for _, port := range outside {
				taken[port] = true
			}
		}
	}
	return nil, fmt.Errorf("create container %s: %d host ports in a row were taken: %w", name, maxRelayAttempts, errors.Join(last))
}
