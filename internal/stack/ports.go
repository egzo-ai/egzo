// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// controlPort is a declared port as the control sidecar takes it (see control.Port): its number and protocol, and the address people use for
// it on the local machine when the instance has a relay.
type controlPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Local    string `json:"local,omitempty"`
}

// putControlPorts tells the control sidecar which ports an instance declares, with the local address of each. Every agent with ports is told,
// relay or not: control enforces this list, and an agent with no list has no ports.
func putControlPorts(ctx context.Context, c *engine.Client, project, name string, ports map[string]config.ResolvedPort, local map[string]string) error {
	if len(ports) == 0 {
		return nil
	}
	body := map[string]controlPort{}
	for portName, port := range ports {
		body[portName] = controlPort{Port: port.Port, Protocol: port.Protocol, Local: local[portName]}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if _, err := ControlRequest(ctx, c, project, "PUT", "/v1/agents/"+name+"/ports", data); err != nil {
		return fmt.Errorf("tell control the ports of %s: %w", name, err)
	}
	return nil
}
