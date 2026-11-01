// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// The variables the stack gives a relay.
const (
	EnvTarget  = "EGZO_RELAY_TARGET"  // the agent's container name
	EnvPorts   = "EGZO_RELAY_PORTS"   // the declared ports, as JSON
	EnvSubnets = "EGZO_RELAY_SUBNETS" // the subnets of the network the outside reaches the relay through, separated by commas
)

// ControlGate asks the control sidecar, as the agent the relay belongs to, whether a port is open for the local machine.
func ControlGate(client *http.Client, base, agent, token string) Gate {
	endpoint := strings.TrimRight(base, "/") + "/v1/expose"
	return func(ctx context.Context, name string) (bool, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false, err
		}
		request.SetBasicAuth(agent, token)
		response, err := client.Do(request)
		if err != nil {
			return false, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return false, fmt.Errorf("the control sidecar answered %s", response.Status)
		}
		var open []struct {
			Name  string `json:"name"`
			Where string `json:"where"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&open); err != nil {
			return false, err
		}
		for _, o := range open {
			if o.Name == name && o.Where == "local" {
				return true, nil
			}
		}
		return false, nil
	}
}

// ListenAddress is the address of this container inside one of subnets: the one network the outside reaches the relay through. The relay never
// listens on the agent's network, so the agent cannot connect to it.
func ListenAddress(subnets []*net.IPNet, addrs []net.Addr) (net.IP, bool) {
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil {
			continue
		}
		for _, subnet := range subnets {
			if subnet.Contains(ipNet.IP) {
				return ipNet.IP, true
			}
		}
	}
	return nil, false
}

// Run is the `egzo relay` role: it reads its settings from the environment, waits for its address on the outside network (the agent's network
// is connected before it starts, and the outside one may take a moment), and carries connections until it is stopped.
func Run() error {
	target := os.Getenv(EnvTarget)
	var ports []Port
	if err := json.Unmarshal([]byte(os.Getenv(EnvPorts)), &ports); err != nil || target == "" || len(ports) == 0 {
		return fmt.Errorf("%s and %s must name the agent and its ports", EnvTarget, EnvPorts)
	}
	var subnets []*net.IPNet
	for _, text := range strings.Split(os.Getenv(EnvSubnets), ",") {
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		_, subnet, err := net.ParseCIDR(text)
		if err != nil {
			return fmt.Errorf("%s: %w", EnvSubnets, err)
		}
		subnets = append(subnets, subnet)
	}
	base, agent, token := os.Getenv("EGZO_CONTROL_URL"), os.Getenv("EGZO_AGENT"), os.Getenv("EGZO_TOKEN")
	if parsed, err := url.Parse(base); err != nil || parsed.Host == "" || agent == "" || token == "" {
		return errors.New("EGZO_CONTROL_URL, EGZO_AGENT and EGZO_TOKEN must be set")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var listen net.IP
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}
		var ok bool
		if listen, ok = ListenAddress(subnets, addrs); ok {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("this container has no address in %s", os.Getenv(EnvSubnets))
		}
	}
	r, err := Start(ctx, Options{
		Listen: listen, Target: target, Ports: ports,
		Gate: ControlGate(&http.Client{Timeout: GateTimeout}, base, agent, token),
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	r.Close()
	return nil
}
