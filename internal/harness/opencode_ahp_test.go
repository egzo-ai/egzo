// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func ahpOptions(t *testing.T) Options {
	o := options(t)
	o.AHP = true
	o.Workdir = "/workspace/scratch"
	o.Exists = func(path string) bool { return path == OpenCodeAHPPlugin }
	o.ReadFile = os.ReadFile
	return o
}

// OpenCode makes a session for a first prompt only: an agent in AHP mode with no task would have no main session, and its chat nothing to
// show or to send a first prompt to. egzo names the main session when the agent starts, keeps the name, and starts the TUI on it.
func TestOpenCodeInAHPModeStartsItsTUIOnAMainSessionItNames(t *testing.T) {
	o := ahpOptions(t)
	mainFile := filepath.Join(o.Home, ".egzo", "main-session")
	plan, err := openCode{}.Plan(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	written, ok := plan.Files[mainFile]
	if !ok {
		t.Fatalf("the main session is named before the harness starts: %v", plan.Files)
	}
	main := strings.TrimSpace(string(written.Content))
	if !strings.HasPrefix(main, "ses") || !validOpenCodeSession(main) {
		t.Fatalf("main session %q is not an OpenCode session id", main)
	}
	if !reflect.DeepEqual(plan.Command, []string{"opencode", "-s", main}) {
		t.Errorf("command = %v: the TUI starts on the main session", plan.Command)
	}
	if plan.Env["EGZO_AHP_DIRECTORY"] != "/workspace/scratch" {
		t.Errorf("the plugin makes the session where the agent works: env = %v", plan.Env)
	}

	// a restart keeps it
	if err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	again, _ := openCode{}.Plan(o, nil)
	if _, rewritten := again.Files[mainFile]; rewritten {
		t.Error("a main session already named is kept, not named again")
	}
	if !reflect.DeepEqual(again.Command, []string{"opencode", "-s", main}) {
		t.Errorf("after a restart, command = %v", again.Command)
	}

	// another one for another agent
	other, _ := openCode{}.Plan(ahpOptions(t), nil)
	if reflect.DeepEqual(other.Command, plan.Command) {
		t.Error("two agents got the same main session")
	}
}

func TestOpenCodeInAHPModeLeavesAChosenSessionOrAnotherCommandAlone(t *testing.T) {
	for _, args := range [][]string{
		{"opencode", "--continue"},
		{"opencode", "-c"},
		{"opencode", "-s", "ses_mine"},
		{"opencode", "--session=ses_mine"},
		{"opencode", "mini"},
		{"opencode", "run", "hello"},
		{"bash"},
	} {
		plan, _ := openCode{}.Plan(ahpOptions(t), args)
		if !reflect.DeepEqual(plan.Command, args) {
			t.Errorf("%v became %v", args, plan.Command)
		}
	}
	// flags and a directory are still the TUI
	plan, _ := openCode{}.Plan(ahpOptions(t), []string{"/usr/local/bin/opencode", "--auto", "."})
	if len(plan.Command) != 5 || plan.Command[1] != "-s" || plan.Command[3] != "--auto" {
		t.Errorf("command = %v", plan.Command)
	}
}

func TestOpenCodeOutOfAHPModeNamesNoSession(t *testing.T) {
	o := ahpOptions(t)
	o.AHP = false
	plan, _ := openCode{}.Plan(o, nil)
	if _, named := plan.Files[filepath.Join(o.Home, ".egzo", "main-session")]; named || !reflect.DeepEqual(plan.Command, []string{"opencode"}) {
		t.Errorf("command = %v, files = %v", plan.Command, plan.Files)
	}
	// nor without the plugin in the image
	o = ahpOptions(t)
	o.Exists = func(string) bool { return false }
	plan, _ = openCode{}.Plan(o, nil)
	if !reflect.DeepEqual(plan.Command, []string{"opencode"}) {
		t.Errorf("command = %v", plan.Command)
	}
}

func TestOpenCodePluginMakesTheMainSessionBeforeItSaysItIsReady(t *testing.T) {
	o := ahpOptions(t)
	plan, _ := openCode{}.Plan(o, nil)
	plugin := string(plan.Files[filepath.Join(o.Home, ".config", "opencode", "plugins", "egzo.js")].Content)
	create, ready := strings.Index(plugin, "ctx.session.create("), strings.Index(plugin, "writeFileSync(readyFile")
	if create < 0 || ready < 0 || create > ready {
		t.Errorf("the plugin makes the main session (ctx.session.create) before it writes the ready file: create at %d, ready at %d", create, ready)
	}
}

// The TUI shows a session it is started on (-s) only if the session is there when it starts: one made after stays off its screen until
// someone types in it. So the background server is started and the main session made before the TUI, by the plan's setup commands, with
// the harness's environment (the AHP plugin's settings among it).
func TestOpenCodeInAHPModeMakesTheMainSessionBeforeTheTUIStarts(t *testing.T) {
	o := ahpOptions(t)
	plan, _ := openCode{}.Plan(o, nil)
	main := plan.Command[2]
	if len(plan.Setup) != 2 || !reflect.DeepEqual(plan.Setup[0], []string{"opencode", "service", "start"}) {
		t.Fatalf("setup = %v: the background server first", plan.Setup)
	}
	create := strings.Join(plan.Setup[1], " ")
	if !strings.Contains(create, "opencode api POST /api/session") || !strings.Contains(create, `"id":"`+main+`"`) ||
		!strings.Contains(create, `"directory":"/workspace/scratch"`) {
		t.Errorf("setup makes the main session %s in the agent's directory: %v", main, plan.Setup[1])
	}
	// a restart makes it again, which OpenCode takes as a no-op for a session that is there
	again, _ := openCode{}.Plan(o, nil)
	if len(again.Setup) != 2 {
		t.Errorf("after a restart, setup = %v", again.Setup)
	}
	// a command that is not the TUI on the main session gets no setup
	other, _ := openCode{}.Plan(ahpOptions(t), []string{"opencode", "--continue"})
	if len(other.Setup) != 0 {
		t.Errorf("setup = %v for a command that picks its own session", other.Setup)
	}
}
