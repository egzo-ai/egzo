// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"flag"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/golden"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var update = flag.Bool("update", false, "rewrite the golden files with what egzo produces now")

// startGolden is where each way an agent starts is shown in full; internal/harness checks the system prompt in the same files.
func startGolden(name string) string {
	return filepath.Join("..", "harness", "testdata", "start", name+".md")
}

// firstPrompt is what the holder types first into an idle agent's terminal: control's first answer to its claim.
func firstPrompt(t *testing.T, r *rig) string {
	t.Helper()
	r.srv.setActivity("coder", activityIdle)
	got, err := r.srv.claim("coder", 0)
	if err != nil {
		t.Fatal(err)
	}
	return got.Line
}

func TestTheFirstPromptOfEachWayAnAgentStartsIsExactlyTheGoldenOne(t *testing.T) {
	t.Run("attended, with a task", func(t *testing.T) {
		r := newRig(t)
		if code := r.put("/v1/agents/coder/task", `{"text":"list the open issues"}`); code != http.StatusNoContent {
			t.Fatalf("task: %d", code)
		}
		golden.Check(t, startGolden("attended-task"), "First prompt typed into the terminal", firstPrompt(t, r), *update)
	})
	for _, file := range []string{"attended-no-task", "operator"} {
		t.Run(file, func(t *testing.T) {
			golden.Check(t, startGolden(file), "First prompt typed into the terminal", firstPrompt(t, newRig(t)), *update)
		})
	}
	t.Run("unattended, with a task", func(t *testing.T) {
		r := newRig(t)
		// what stack.Spawn sends for an unattended spawn (controlTask)
		response := r.asOperator("POST", "/v1/messages", `{"from":"user:u7","to":"agent:coder","text":"list the open issues","key":"spawn:i1"}`)
		var queued struct{ ID string }
		decode(t, response, &queued)
		if queued.ID == "" {
			t.Fatal("the task was not queued")
		}
		placeholders := func(text string) string {
			text = strings.ReplaceAll(text, queued.ID, "<id>")
			return regexp.MustCompile(`"time":"[^"]*"`).ReplaceAllString(text, `"time":"<time>"`)
		}
		golden.Check(t, startGolden("unattended-task"), "First prompt typed into the terminal", placeholders(firstPrompt(t, r)), *update)

		session, err := r.mcpSession("coder", r.token("coder"))
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result := call(t, session, "get_message", map[string]any{"id": queued.ID})
		golden.Check(t, startGolden("unattended-task"), "The request, as get_message returns it", placeholders(result.Content[0].(*mcp.TextContent).Text), *update)
	})
}
