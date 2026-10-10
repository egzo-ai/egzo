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

func TestWithoutABrowserTheServerIsDeletedNotRegistered(t *testing.T) {
	base := options(t)
	for name, o := range map[string]Options{"no lookup": base, "not on the PATH": withBrowser(base, false)} {
		claude, _ := claudeCode{}.Plan(o, nil)
		code, _ := openCode{}.Plan(o, nil)
		for harness, servers := range map[string]any{
			"claude-code": get(t, decode(t, claude, filepath.Join(o.Home, ".claude.json")), "mcpServers"),
			"opencode":    get(t, decode(t, code, filepath.Join(o.Home, ".config", "opencode", "opencode.json")), "mcp"),
		} {
			// an explicit null: merging it deletes what an image that had a browser left in the persisted home
			if value, present := servers.(map[string]any)["playwright"]; !present || value != nil {
				t.Errorf("%s, %s: playwright = %v (present %v), want a null", harness, name, value, present)
			}
		}
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
