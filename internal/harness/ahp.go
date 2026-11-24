// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AHP mode: the agent's harness serves the Agent Host Protocol (https://github.com/microsoft/agent-host-protocol) inside its own container,
// on its loopback, and `egzo agent ahp` carries it over stdin and stdout for the project's link. AHP carries the chat of the agent's main
// session, nothing else: egzo's own verbs (attach, messages, ports) are unchanged. It is chosen when the agent is spawned and kept for the
// instance's life.

// AHP is an integration that has an AHP mode.
type AHP interface {
	AHP() AHPMode
}

// AHPMode is what a harness's AHP mode keeps of the agent.
type AHPMode struct {
	// Terminal says the agent's terminal is still there in AHP mode: false for a harness that has to give its terminal up for AHP (one
	// dropped to ACP, which a single client owns over stdio).
	Terminal bool
}

// AHPModeOf says whether a harness has an AHP mode, and which.
func AHPModeOf(name string) (AHPMode, bool) {
	integration, ok := For(name)
	if !ok {
		return AHPMode{}, false
	}
	a, ok := integration.(AHP)
	if !ok {
		return AHPMode{}, false
	}
	return a.AHP(), true
}

// AHPEnv is "1" in the container of an agent spawned in AHP mode.
const AHPEnv = "EGZO_AHP"

// AHPEndpointFile is where, under the agent's home, an integration in AHP mode writes how to reach its AHP server: what `egzo agent ahp`
// reads. Only the agent can read it.
const AHPEndpointFile = ".egzo/ahp.json"

// AHPEndpoint is how to reach the AHP server of an agent, inside its container.
type AHPEndpoint struct {
	// URL is the server's WebSocket address, on the container's loopback.
	URL string `json:"url"`
	// Token is the credential the server asks of its clients, made anew each time the harness starts.
	Token string `json:"token"`
	// MainSession is the file the harness's state plugin writes the agent's main session to, once it has one: the first session it
	// creates. egzo shows that session and no other.
	MainSession string `json:"main_session,omitempty"`
}

// ReadAHPEndpoint reads what an integration wrote under home.
func ReadAHPEndpoint(home string) (AHPEndpoint, error) {
	data, err := os.ReadFile(filepath.Join(home, AHPEndpointFile))
	if os.IsNotExist(err) {
		return AHPEndpoint{}, fmt.Errorf("this agent is not in AHP mode: it was spawned without it, or its image has no AHP server")
	}
	if err != nil {
		return AHPEndpoint{}, err
	}
	var endpoint AHPEndpoint
	if err := json.Unmarshal(data, &endpoint); err != nil || endpoint.URL == "" {
		return AHPEndpoint{}, fmt.Errorf("%s is not what egzo wrote", AHPEndpointFile)
	}
	return endpoint, nil
}

// newAHPToken makes the credential of one run of an AHP server.
func newAHPToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WithAHP lists the harnesses that have an AHP mode, sorted.
func WithAHP() []string {
	var names []string
	for _, name := range Names() {
		if _, ok := AHPModeOf(name); ok {
			names = append(names, name)
		}
	}
	return names
}
