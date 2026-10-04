package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("reads egzo.yaml from the directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte("version: 1\nname: demo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		file, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if file.Version != 1 || file.Name == nil || *file.Name != "demo" {
			t.Errorf("Load = %+v", file)
		}
	})

	t.Run("a missing file names the directory", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), dir) {
			t.Errorf("Load error = %v, want it to mention %s", err, dir)
		}
	})
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name, yaml, wantInError string
	}{
		{"unknown top-level key", "nonsense: 1\n", "nonsense"},
		{"unknown agent key", "agents:\n  a:\n    harness: custom\n    bogus: 1\n", "bogus"},
		{"top-level users", "users:\n  alice: {}\n", "users are never declared"},
		{"nested users", "agents:\n  a:\n    harness: custom\n    users: [x]\n", "hub"},
		{"users inside a sequence", "egress:\n  x:\n    allow:\n      - users: 1\n", "users are never declared"},
		{"malformed yaml", "agents: [\n", "egzo.yaml"},
		{"numeric service", "egress:\n  default:\n    services:\n      anthropic: 42\n", "secret reference"},
		{"list service", "egress:\n  default:\n    services:\n      anthropic: [a]\n", "secret reference"},
		{"unknown service key", "egress:\n  default:\n    services:\n      x: { hosts: [a.com], bogus: 1 }\n", "unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("Parse accepted the file")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error = %q, want it to contain %q", err, c.wantInError)
			}
		})
	}
}

func TestParseUsersErrorCarriesTheLine(t *testing.T) {
	_, err := Parse([]byte("name: x\n\nusers: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "egzo.yaml:3:") {
		t.Errorf("error = %v, want line 3", err)
	}
}

func TestParseServiceEntryShapes(t *testing.T) {
	file, err := Parse([]byte(`
egress:
  default:
    services:
      anthropic: main/KEY
      custom:
        hosts: [api.example.com]
        inject: { header: X-Key }
        secret: main/KEY
        inspect: true
`))
	if err != nil {
		t.Fatal(err)
	}
	services := file.Egress["default"].Services
	if got := services["anthropic"]; got.Ref != "main/KEY" || got.Def != nil {
		t.Errorf("anthropic = %+v, want a reference", got)
	}
	got := services["custom"]
	if got.Ref != "" || got.Def == nil {
		t.Fatalf("custom = %+v, want a definition", got)
	}
	if got.Def.Hosts[0] != "api.example.com" || got.Def.Inject.Header != "X-Key" || got.Def.Secret != "main/KEY" || !got.Def.Inspect {
		t.Errorf("custom definition = %+v", got.Def)
	}
}
