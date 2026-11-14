// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"slices"
	"strings"
	"testing"
)

func TestASpawnsStarterTaskAndUnattendedGoTogether(t *testing.T) {
	for _, c := range []struct {
		req   SpawnRequest
		actor string
		ok    bool
	}{
		{SpawnRequest{}, "operator", true},
		{SpawnRequest{StarterName: "Joe", Task: "list the open issues"}, "user:u7", true},
		{SpawnRequest{StarterName: "Joe", Task: "list the open issues", Unattended: true}, "user:u7", true},
		{SpawnRequest{Task: "a task the operator typed"}, "operator", true},
		{SpawnRequest{Unattended: true}, "user:u7", false},               // nothing to start on, and nobody to type it
		{SpawnRequest{Task: "x", Unattended: true}, "operator", false},   // nobody to report to on a chat platform
		{SpawnRequest{Task: "x", Unattended: true}, "service:ci", false}, // nor here
		{SpawnRequest{StarterName: "Joe"}, "operator", false},            // only a hub user has a name
		{SpawnRequest{StarterName: "two\nlines"}, "user:u7", false},      // one line
		{SpawnRequest{StarterName: "\x1b[2Jcleared"}, "user:u7", false},  // nothing a terminal acts on
		{SpawnRequest{StarterName: strings.Repeat("j", 101)}, "user:u7", false},
		{SpawnRequest{Task: strings.Repeat("t", maxTask+1)}, "user:u7", false},
	} {
		if err := checkStart(c.req, c.actor); (err == nil) != c.ok {
			t.Errorf("%+v by %s: %v, want ok %v", c.req, c.actor, err, c.ok)
		}
	}
}

func TestAnAgentsEnvironmentSaysWhoStartedIt(t *testing.T) {
	if got := starterEnv(InstanceIdentity{}); !slices.Equal(got, []string{"EGZO_STARTER=operator"}) {
		t.Errorf("no actor = %v", got)
	}
	got := starterEnv(InstanceIdentity{Actor: "user:u7", StarterName: "Joe", Unattended: true})
	if want := []string{"EGZO_STARTER=user:u7", "EGZO_STARTER_NAME=Joe", "EGZO_UNATTENDED=1"}; !slices.Equal(got, want) {
		t.Errorf("a hub user's unattended agent = %v, want %v", got, want)
	}
}
