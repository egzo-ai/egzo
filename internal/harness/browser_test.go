// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func withBrowser(o Options, present bool) Options {
	o.LookPath = func(file string) (string, error) {
		if present && file == "playwright-mcp" {
			return "/usr/local/bin/playwright-mcp", nil
		}
		return "", os.ErrNotExist
	}
	return o
}

func marshal(value any) string {
	out, _ := json.Marshal(value)
	return string(out)
}

func TestAHarnessRegistersTheBrowserNextToTheEgzoServerWhenTheImageHasOne(t *testing.T) {
	o := withBrowser(options(t), true)

	plan, _ := claudeCode{}.Plan(o, nil)
	servers := get(t, decode(t, plan, filepath.Join(o.Home, ".claude.json")), "mcpServers")
	want := `{"args":["agent","browser"],"command":"egzo","env":{},"type":"stdio"}`
	if got := marshal(get(t, servers, "playwright")); got != want {
		t.Errorf("claude-code server = %s, want %s", got, want)
	}
	if get(t, servers, "egzo") == nil {
		t.Error("claude-code: the egzo server is gone")
	}

	plan, _ = openCode{}.Plan(o, nil)
	servers = get(t, decode(t, plan, filepath.Join(o.Home, ".config", "opencode", "opencode.json")), "mcp")
	want = `{"command":["egzo","agent","browser"],"enabled":true,"type":"local"}`
	if got := marshal(get(t, servers, "playwright")); got != want {
		t.Errorf("opencode server = %s, want %s", got, want)
	}
	if get(t, servers, "egzo") == nil {
		t.Error("opencode: the egzo server is gone")
	}
}

func TestAStaleBrowserServerIsRemovedFromAPersistedHomeAndWhatTheUserAddedStays(t *testing.T) {
	cases := []struct {
		name        string
		integration Integration
		path        []string
		key         string
		existing    string
	}{
		{"claude-code", claudeCode{}, []string{".claude.json"}, "mcpServers",
			`{"mcpServers":{"playwright":{"type":"stdio","command":"egzo"},"mine":{"type":"stdio","command":"x"}}}`},
		{"opencode", openCode{}, []string{".config", "opencode", "opencode.json"}, "mcp",
			`{"mcp":{"playwright":{"type":"local","command":["egzo"]},"mine":{"type":"local","command":["x"]}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := options(t)
			path := filepath.Join(append([]string{o.Home}, c.path...)...)
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, []byte(c.existing), 0o600)
			servers := func() map[string]any {
				var config map[string]any
				data, _ := os.ReadFile(path)
				json.Unmarshal(data, &config)
				return config[c.key].(map[string]any)
			}

			plan, _ := c.integration.Plan(withBrowser(o, false), nil)
			if err := plan.Write(); err != nil {
				t.Fatal(err)
			}
			if _, stale := servers()["playwright"]; stale {
				t.Errorf("the stale playwright server is still there: %v", servers())
			}
			if servers()["mine"] == nil || servers()["egzo"] == nil {
				t.Errorf("a server the user added, or egzo's own, is gone: %v", servers())
			}

			plan, _ = c.integration.Plan(withBrowser(o, true), nil)
			if err := plan.Write(); err != nil {
				t.Fatal(err)
			}
			if servers()["playwright"] == nil || servers()["mine"] == nil {
				t.Errorf("the browser came back without the user's server: %v", servers())
			}
		})
	}
}

// browserCase is one harness, as a round trip through the file the plan merges into.
type browserCase struct {
	name        string
	integration Integration
	path        []string
	key         string
}

var browserCases = []browserCase{
	{"claude-code", claudeCode{}, []string{".claude.json"}, "mcpServers"},
	{"opencode", openCode{}, []string{".config", "opencode", "opencode.json"}, "mcp"},
}

// roundTrip writes existing (when not empty) where the harness keeps it, applies the plan and returns the servers.
func roundTrip(t *testing.T, c browserCase, existing string, browser bool) map[string]any {
	t.Helper()
	o := options(t)
	path := filepath.Join(append([]string{o.Home}, c.path...)...)
	if existing != "" {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(existing), 0o600)
	}
	plan, err := c.integration.Plan(withBrowser(o, browser), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config[c.key].(map[string]any)
}

func TestAFreshHomeGetsTheBrowserServer(t *testing.T) {
	want := map[string]string{
		"claude-code": `{"args":["agent","browser"],"command":"egzo","env":{},"type":"stdio"}`,
		"opencode":    `{"command":["egzo","agent","browser"],"enabled":true,"type":"local"}`,
	}
	for _, c := range browserCases {
		servers := roundTrip(t, c, "", true)
		if got := marshal(servers["playwright"]); got != want[c.name] {
			t.Errorf("%s: playwright = %s, want %s", c.name, got, want[c.name])
		}
		if servers["egzo"] == nil {
			t.Errorf("%s: the egzo server is missing", c.name)
		}
	}
}

func TestAnEntryEgzoWroteIsReplacedWholesaleNotMerged(t *testing.T) {
	existing := map[string]string{
		"claude-code": `{"mcpServers":{"playwright":{"type":"stdio","command":"egzo","args":["agent","old","--x"],"env":{"A":"1"}},"mine":{"command":"x"}}}`,
		"opencode":    `{"mcp":{"playwright":{"type":"local","command":["egzo","agent","old"],"enabled":false,"environment":{"A":"1"}},"mine":{"command":["x"]}}}`,
	}
	want := map[string]string{
		"claude-code": `{"args":["agent","browser"],"command":"egzo","env":{},"type":"stdio"}`,
		"opencode":    `{"command":["egzo","agent","browser"],"enabled":true,"type":"local"}`,
	}
	for _, c := range browserCases {
		servers := roundTrip(t, c, existing[c.name], true)
		if got := marshal(servers["playwright"]); got != want[c.name] {
			t.Errorf("%s: playwright = %s, want %s", c.name, got, want[c.name])
		}
		if servers["mine"] == nil || servers["egzo"] == nil {
			t.Errorf("%s: another server was touched: %v", c.name, servers)
		}
	}
}

func TestAServerTheUserWroteIsLeftAloneWhateverTheImageHas(t *testing.T) {
	existing := map[string]string{
		"claude-code": `{"mcpServers":{"playwright":{"type":"stdio","command":"npx","args":["@playwright/mcp@latest","--headless"],"env":{"A":"1"}}}}`,
		"opencode":    `{"mcp":{"playwright":{"type":"local","command":["npx","@playwright/mcp"],"enabled":false}}}`,
	}
	for _, c := range browserCases {
		for _, browser := range []bool{true, false} {
			servers := roundTrip(t, c, existing[c.name], browser)
			var want map[string]any
			json.Unmarshal([]byte(existing[c.name]), &want)
			if got, wanted := marshal(servers["playwright"]), marshal(want[c.key].(map[string]any)["playwright"]); got != wanted {
				t.Errorf("%s (browser %v): playwright = %s, want it untouched: %s", c.name, browser, got, wanted)
			}
			if servers["egzo"] == nil {
				t.Errorf("%s: the egzo server is missing", c.name)
			}
		}
	}
}

func TestWithoutABrowserOnlyAnEntryEgzoWroteIsDeleted(t *testing.T) {
	for _, c := range browserCases {
		fresh := roundTrip(t, c, "", false)
		if _, present := fresh["playwright"]; present {
			t.Errorf("%s: a server for a browser the image lacks: %v", c.name, fresh)
		}
	}
	// the other test (a stale server is removed) covers the entry egzo wrote
}
