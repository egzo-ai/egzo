// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
)

func TestOutsidePortsAreStableInThePoolAndSkipWhatIsTaken(t *testing.T) {
	names := []string{"db", "voice", "web"}
	first, err := outsidePorts("01JBX7Q0000000000000000000", names, 20000, 29999, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := outsidePorts("01JBX7Q0000000000000000000", names, 20000, 29999, nil)
	if len(first) != 3 || first["db"] != again["db"] || first["web"] != again["web"] {
		t.Errorf("not stable: %v %v", first, again)
	}
	seen := map[int]bool{}
	for name, port := range first {
		if port < 20000 || port > 29999 || seen[port] {
			t.Errorf("%s: %d", name, port)
		}
		seen[port] = true
	}
	other, _ := outsidePorts("01JBX7Q0000000000000000001", names, 20000, 29999, nil)
	if other["db"] == first["db"] && other["web"] == first["web"] && other["voice"] == first["voice"] {
		t.Error("two instances chose the same ports")
	}
	taken := map[int]bool{first["db"]: true}
	skipped, _ := outsidePorts("01JBX7Q0000000000000000000", names, 20000, 29999, taken)
	if skipped["db"] == first["db"] {
		t.Error("a port in use by another relay was chosen")
	}
}

func TestOutsidePortsWrapAroundThePoolAndRunOut(t *testing.T) {
	got, err := outsidePorts("x", []string{"a", "b", "c"}, 100, 102, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	slices.Sort([]int{})
	if _, err := outsidePorts("x", []string{"a", "b"}, 100, 100, nil); err == nil || !strings.Contains(err.Error(), "free") {
		t.Errorf("a pool of one port gave two: %v", err)
	}
	if _, err := outsidePorts("x", []string{"a"}, 100, 101, map[int]bool{100: true, 101: true}); err == nil {
		t.Error("a pool that is all taken gave a port")
	}
}

func TestOnlyTheEnginesWordsForATakenPortAreAConflict(t *testing.T) {
	for _, text := range []string{"driver failed programming external connectivity: Bind for 127.0.0.1:20001 failed: port is already allocated", "listen tcp4 127.0.0.1:20001: bind: address already in use"} {
		if !portConflict(errors.New(text)) {
			t.Errorf("%q", text)
		}
	}
	if portConflict(errors.New("no such network")) || portConflict(nil) {
		t.Error("another failure was taken for a conflict")
	}
}

func TestTheLocalAddressIsAURLForHTTPAndHostAndPortForTheRest(t *testing.T) {
	local := &config.ResolvedLocalPorts{Host: "mybox.lan"}
	if got := localAddress(local, config.ResolvedPort{Port: 80, Protocol: "http"}, 20001); got != "http://mybox.lan:20001/" {
		t.Error(got)
	}
	if got := localAddress(local, config.ResolvedPort{Port: 5432, Protocol: "tcp"}, 20002); got != "mybox.lan:20002" {
		t.Error(got)
	}
	if got := localAddress(&config.ResolvedLocalPorts{Host: "::1"}, config.ResolvedPort{Port: 5432, Protocol: "udp"}, 20003); got != "[::1]:20003" {
		t.Error(got)
	}
}

func TestTheRelayIsHardenedOnTheLocalPortsNetworkAndJoinsTheAgentsBeforeItStarts(t *testing.T) {
	project := desireProject()
	coder := project.Agents["coder"]
	coder.Ports = map[string]config.ResolvedPort{"web": {Port: 5173, Protocol: "http"}, "voice": {Port: 5432, Protocol: "udp"}}
	project.Agents["coder"] = coder
	project.LocalPorts = &config.ResolvedLocalPorts{Bind: "127.0.0.1", Host: "127.0.0.1", PoolLow: 20000, PoolHigh: 29999, MaxExposed: 10}
	in := Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "tok"}, Relay: map[string]RelayInput{"coder": {Outside: map[string]int{"web": 20001, "voice": 20002}}}}
	desired, err := DesireInstance(project, "/p", in, "coder", InstanceIdentity{ID: "01JBX7Q0000000000000000000", Template: "coder", Actor: "operator", TemplateHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Containers) != 2 {
		t.Fatalf("%d containers", len(desired.Containers))
	}
	relay := desired.Containers[1]
	if relay.Name != "proj-coder-relay-1" || relay.Identity.Kind != "relay" || relay.Identity.Instance != "coder" {
		t.Errorf("%+v", relay.Identity)
	}
	if relay.Network != "proj_local_ports" || relay.ExtraNetwork != "proj_coder" || relay.User != sidecarUser || !relay.ReadonlyRootfs ||
		relay.Restart != "unless-stopped" || relay.Memory != 64<<20 || relay.PidsLimit != 64 || len(relay.Mounts) != 0 {
		t.Errorf("%+v", relay)
	}
	want := []PublishedPort{{Port: 5432, Protocol: "udp", HostIP: "127.0.0.1", HostPort: 20002}, {Port: 5173, Protocol: "tcp", HostIP: "127.0.0.1", HostPort: 20001}}
	if !slices.Equal(relay.Publish, want) {
		t.Errorf("%+v", relay.Publish)
	}
	for _, line := range relay.Env {
		if strings.Contains(line, "SECRET") || strings.Contains(line, "docker.sock") {
			t.Errorf("the relay holds %s", line)
		}
	}
	if without := desireWithoutRelay(t, project); len(without.Containers) != 1 {
		t.Errorf("an instance without a relay input got %d containers", len(without.Containers))
	}
	if agentNetwork := desired.Networks[0]; !agentNetwork.Internal {
		t.Error("the agent's network must stay internal")
	}
}

func desireWithoutRelay(t *testing.T, project *config.Resolved) Desired {
	t.Helper()
	desired, err := DesireInstance(project, "/p", Inputs{Image: "egzo:test", Tokens: map[string]string{"coder": "tok"}}, "coder", InstanceIdentity{Template: "coder"})
	if err != nil {
		t.Fatal(err)
	}
	return desired
}

func TestTheProjectHasANetworkForLocalPortsOnlyWhenItTurnsThemOn(t *testing.T) {
	project := desireProject()
	desired, err := Desire(project, "/p", Inputs{Image: "egzo:test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range desired.Networks {
		if strings.HasSuffix(n.Name, "_local_ports") {
			t.Error("a project without local ports got the network")
		}
	}
	for _, c := range desired.Containers {
		for _, e := range c.Env {
			if strings.HasPrefix(e, "EGZO_LOCAL_MAX_EXPOSED") {
				t.Error("control was told about local ports of a project without them")
			}
		}
	}
	project.LocalPorts = &config.ResolvedLocalPorts{Bind: "127.0.0.1", Host: "127.0.0.1", PoolLow: 20000, PoolHigh: 29999, MaxExposed: 3}
	desired, err = Desire(project, "/p", Inputs{Image: "egzo:test"})
	if err != nil {
		t.Fatal(err)
	}
	var network *NetworkSpec
	for i := range desired.Networks {
		if desired.Networks[i].Name == "proj_local_ports" {
			network = &desired.Networks[i]
		}
	}
	if network == nil || network.Internal {
		t.Fatalf("%+v", network)
	}
	if !slices.Contains(desired.Containers[0].Env, "EGZO_LOCAL_MAX_EXPOSED=3") {
		t.Errorf("%v", desired.Containers[0].Env)
	}
}

func TestUpKeepsTheRelaysOfInstancesLikeTheirAgents(t *testing.T) {
	project := desireProject()
	desired, _ := Desire(project, "/p", Inputs{Image: "egzo:test"})
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "proj-coder-relay-1", Kind: kindRelay, Instance: "coder", State: "running", ID: "r"},
		{Type: "container", Name: "proj-coder", Kind: kindAgent, Instance: "coder", State: "running", ID: "a"},
	}}
	for _, action := range BuildPlan(desired, observed, false) {
		if action.Verb == "remove" && strings.Contains(action.Name, "coder") {
			t.Errorf("up would %s", action)
		}
	}
}
