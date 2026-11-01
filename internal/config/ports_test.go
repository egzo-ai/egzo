// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import "testing"

func TestParsePort(t *testing.T) {
	for raw, want := range map[string]ResolvedPort{
		"5173/http": {5173, "http"}, "5432/tcp": {5432, "tcp"}, "1/udp": {1, "udp"}, "65535/tcp": {65535, "tcp"},
	} {
		if got, ok := ParsePort(raw); !ok || got != want {
			t.Errorf("ParsePort(%q) = %v, %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"", "80", "80/", "/tcp", "0/tcp", "65536/tcp", "80/ftp", "080/tcp", "+80/tcp", "80/tcp/x"} {
		if _, ok := ParsePort(raw); ok {
			t.Errorf("ParsePort(%q) accepted", raw)
		}
	}
}

func TestPortsAndLocalPortsResolve(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), `
local_ports: { bind: 127.0.0.1 }
agents:
  web: { harness: claude-code, ports: { app: "5173/http", db: "5432/tcp", voice: "5432/udp" } }
`)
	if got := resolved.Agents["web"].Ports["voice"]; got != (ResolvedPort{5432, "udp"}) {
		t.Errorf("voice = %v", got)
	}
	if lp := resolved.LocalPorts; lp.Host != "127.0.0.1" || lp.PoolLow != 20000 || lp.PoolHigh != 29999 || lp.MaxExposed != 10 {
		t.Errorf("defaults = %+v", lp)
	}
}

func TestPortsRefusals(t *testing.T) {
	agent := func(ports string) string { return "agents:\n  web: { harness: claude-code, ports: " + ports + " }\n" }
	local := "local_ports: { bind: 127.0.0.1 }\n"
	cases := []struct{ name, yaml, want string }{
		{"no protocol", local + agent(`{ a: "80" }`), "PORT/PROTOCOL"},
		{"bad name", local + agent(`{ A: "80/http" }`), "port name"},
		{"twice", local + agent(`{ a: "80/http", b: "80/tcp" }`), "twice"},
		{"tcp without local", agent(`{ a: "80/tcp" }`), "local_ports"},
		{"http without anything", agent(`{ a: "80/http" }`), "could never open"},
		{"no bind", "local_ports: { host: x }\n", "bind is required"},
		{"wildcard without host", "local_ports: { bind: '::' }\n", "host is required"},
		{"bad pool", "local_ports: { bind: 127.0.0.1, pool: 5-1 }\n", "pool"},
		{"max_exposed 0", "local_ports: { bind: 127.0.0.1, max_exposed: 0 }\n", "max_exposed"},
		{"too many for max_exposed", "local_ports: { bind: 127.0.0.1, max_exposed: 1 }\n" + agent(`{ a: "80/tcp", b: "81/tcp" }`), "could not all be open"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantProblems(t, t.TempDir(), c.yaml, c.want) })
	}
}
