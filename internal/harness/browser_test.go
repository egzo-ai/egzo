// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	servers = get(t, decode(t, plan, filepath.Join(o.Home, ".config", "opencode", "opencode.json")), "mcp", "servers")
	want = `{"command":["egzo","agent","browser"],"type":"local"}`
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
			`{"mcpServers":{"playwright":{"type":"stdio","command":"egzo","args":["agent","browser"]},"mine":{"type":"stdio","command":"x"}}}`},
		{"opencode", openCode{}, []string{".config", "opencode", "opencode.json"}, "mcp.servers",
			`{"mcp":{"servers":{"playwright":{"type":"local","command":["egzo","agent","browser"]},"mine":{"type":"local","command":["x"]}}}}`},
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
				return at(config, c.key)
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
	{"opencode", openCode{}, []string{".config", "opencode", "opencode.json"}, "mcp.servers"},
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
	return at(config, c.key)
}

func TestAFreshHomeGetsTheBrowserServer(t *testing.T) {
	want := map[string]string{
		"claude-code": `{"args":["agent","browser"],"command":"egzo","env":{},"type":"stdio"}`,
		"opencode":    `{"command":["egzo","agent","browser"],"type":"local"}`,
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
		"claude-code": `{"mcpServers":{"playwright":{"type":"stdio","command":"egzo","args":["agent","browser","--old"],"env":{"A":"1"}},"mine":{"command":"x"}}}`,
		"opencode":    `{"mcp":{"servers":{"playwright":{"type":"local","command":["egzo","agent","browser","--old"],"disabled":true,"environment":{"A":"1"}},"mine":{"command":["x"]}}}}`,
	}
	want := map[string]string{
		"claude-code": `{"args":["agent","browser"],"command":"egzo","env":{},"type":"stdio"}`,
		"opencode":    `{"command":["egzo","agent","browser"],"type":"local"}`,
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
		"opencode":    `{"mcp":{"servers":{"playwright":{"type":"local","command":["npx","@playwright/mcp"],"disabled":true}}}}`,
	}
	for _, c := range browserCases {
		for _, browser := range []bool{true, false} {
			servers := roundTrip(t, c, existing[c.name], browser)
			var want map[string]any
			json.Unmarshal([]byte(existing[c.name]), &want)
			if got, wanted := marshal(servers["playwright"]), marshal(at(want, c.key)["playwright"]); got != wanted {
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

func TestOnlyAnEntryThatLooksLikeEgzosOwnIsEgzos(t *testing.T) {
	for _, c := range []struct {
		name     string
		entry    string
		claude   bool
		opencode bool
	}{
		{"egzo's own", `{"command":"egzo","args":["agent","browser"]}`, true, false},
		{"egzo's own with more arguments", `{"command":"egzo","args":["agent","browser","--x"]}`, true, false},
		{"egzo's own for OpenCode", `{"command":["egzo","agent","browser"]}`, false, true},
		{"egzo's own for OpenCode with more", `{"command":["egzo","agent","browser","--x"]}`, false, true},
		{"egzo with other arguments", `{"command":"egzo","args":["something","else"]}`, false, false},
		{"egzo without arguments", `{"command":"egzo"}`, false, false},
		{"egzo with other arguments for OpenCode", `{"command":["egzo","mine"]}`, false, false},
		{"egzo alone for OpenCode", `{"command":["egzo"]}`, false, false},
		{"egzo agent only", `{"command":["egzo","agent"]}`, false, false},
		{"another program", `{"command":"npx","args":["agent","browser"]}`, false, false},
		{"another program for OpenCode", `{"command":["npx","egzo","agent","browser"]}`, false, false},
		{"not an object", `"egzo"`, false, false},
		{"arguments of the wrong type", `{"command":"egzo","args":"agent browser"}`, false, false},
	} {
		var entry any
		if err := json.Unmarshal([]byte(c.entry), &entry); err != nil {
			t.Fatal(err)
		}
		if got := ownsClaudeCodeBrowser(entry); got != c.claude {
			t.Errorf("%s: Claude Code owns = %v, want %v", c.name, got, c.claude)
		}
		if got := ownsOpenCodeBrowser(entry); got != c.opencode {
			t.Errorf("%s: OpenCode owns = %v, want %v", c.name, got, c.opencode)
		}
	}
}

// at is the object at a dotted key of a decoded config ("mcp.servers").
func at(config map[string]any, key string) map[string]any {
	object := config
	for _, part := range strings.Split(key, ".") {
		object, _ = object[part].(map[string]any)
	}
	return object
}

func TestOpenCodeMovesWhatItsOldVersionWroteToTheNewNames(t *testing.T) {
	o := withBrowser(options(t), true)
	path := filepath.Join(o.Home, ".config", "opencode", "opencode.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	old := `{"autoupdate":false,"permission":{"*":"allow"},"mcp":{"egzo":{"type":"remote","url":"http://x/mcp"},"playwright":{"type":"local","command":["egzo","agent","browser"],"enabled":true},"mine":{"type":"local","command":["x"]}}}`
	os.WriteFile(path, []byte(old), 0o600)
	plan, _ := openCode{}.Plan(o, nil)
	if err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &config)
	for _, key := range []string{"autoupdate", "permission"} {
		if _, stale := config[key]; stale {
			t.Errorf("%s of OpenCode 1 is still there: %s", key, data)
		}
	}
	mcp := at(config, "mcp")
	if mcp["egzo"] != nil || mcp["playwright"] != nil {
		t.Errorf("egzo's servers are still under the old place: %v", mcp)
	}
	if mcp["mine"] == nil {
		t.Errorf("a server of the user's was deleted: %v", mcp)
	}
	if servers := at(config, "mcp.servers"); servers["egzo"] == nil || servers["playwright"] == nil {
		t.Errorf("servers = %v", servers)
	}
}
