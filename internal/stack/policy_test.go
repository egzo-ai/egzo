// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/proxy"
)

func policyProject() *config.Resolved {
	return &config.Resolved{
		Agents: map[string]config.ResolvedAgent{
			"coder":  {Egress: "default"},
			"review": {Egress: "tight"},
		},
		Egress: map[string]*config.ResolvedProfile{
			"default": {
				Allow: []string{"pypi.org"},
				Services: map[string]*config.ResolvedService{
					"anthropic": {Hosts: []string{"api.anthropic.com"}, Inject: &config.Inject{Header: "x-api-key"}, Secret: "main/KEY"},
					"plain":     {Hosts: []string{"docs.example"}, Inspect: true},
				},
			},
			"tight": {},
		},
	}
}

func TestBuildPolicy(t *testing.T) {
	policy := BuildPolicy(policyProject(), map[string]string{"main/KEY": "s3cret"})

	coder := policy.Profiles["default"]
	if !reflect.DeepEqual(coder.Allow, []string{"pypi.org"}) {
		t.Errorf("default = %+v", coder)
	}
	want := []proxy.Service{
		{Name: "anthropic", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Secret: "s3cret"},
		{Name: "plain", Hosts: []string{"docs.example"}, Inspect: true},
	}
	if !reflect.DeepEqual(coder.Services, want) {
		t.Errorf("default services = %+v\nwant %+v", coder.Services, want)
	}

	tight := policy.Profiles["tight"]
	if len(tight.Allow) != 0 || len(tight.Services) != 0 {
		t.Errorf("tight = %+v, want nothing allowed", tight)
	}
	if tight.Allow == nil {
		t.Error("Allow is nil: it would marshal as null instead of []")
	}
}

func TestThePolicyHoldsOnlyTheProfilesSomeAgentUses(t *testing.T) {
	project := policyProject()
	project.Egress["unused"] = &config.ResolvedProfile{Allow: []string{"never.example"}}
	policy := BuildPolicy(project, nil)
	if _, ok := policy.Profiles["unused"]; ok || len(policy.Profiles) != 2 {
		t.Errorf("profiles = %v", policy.Profiles)
	}
}

func TestTheBindingOfAnInstanceHoldsNoSecret(t *testing.T) {
	binding := BindingFor("tok", "default")
	if binding.Token != "tok" || binding.Profile != "default" {
		t.Errorf("binding = %+v", binding)
	}
}

func TestBuildPolicyDoesNotAliasTheProfile(t *testing.T) {
	project := policyProject()
	policy := BuildPolicy(project, nil)
	profile := policy.Profiles["default"]
	profile.Allow[0] = "mutated.example"
	if project.Egress["default"].Allow[0] != "pypi.org" {
		t.Error("mutating the policy changed the resolved profile")
	}
}

func TestBuildPolicyHash(t *testing.T) {
	secrets := map[string]string{"main/KEY": "one"}
	base := BuildPolicy(policyProject(), secrets)

	if base.Hash == "" || len(base.Hash) != 64 {
		t.Fatalf("hash = %q, want a sha256 hex digest", base.Hash)
	}
	if again := BuildPolicy(policyProject(), secrets); again.Hash != base.Hash {
		t.Error("the hash is not deterministic")
	}
	if changed := BuildPolicy(policyProject(), map[string]string{"main/KEY": "two"}); changed.Hash == base.Hash {
		t.Error("a new secret value did not change the hash")
	}
}

func TestResolveSecrets(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("key.txt", "from-file\r\n\n")
	write("empty.txt", "\n")
	t.Setenv("EGZO_TEST_KEY", "from-env")
	t.Setenv("EGZO_TEST_EMPTY", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "homekey"), []byte("from-home"), 0o600); err != nil {
		t.Fatal(err)
	}

	project := func(sources map[string]string) *config.Resolved {
		services := map[string]*config.ResolvedService{}
		for ref := range sources {
			services[ref] = &config.ResolvedService{Inject: &config.Inject{Header: "H"}, Secret: ref}
		}
		return &config.Resolved{
			SecretSources: sources,
			Agents:        map[string]config.ResolvedAgent{"a": {Egress: "default"}},
			Egress:        map[string]*config.ResolvedProfile{"default": {Services: services}},
		}
	}

	t.Run("reads every supported source", func(t *testing.T) {
		got, err := ResolveSecrets(project(map[string]string{
			"v/env":      "env:EGZO_TEST_KEY",
			"v/file":     "file:key.txt",
			"v/absolute": "file:" + filepath.Join(dir, "key.txt"),
			"v/home":     "file:~/homekey",
		}), dir)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"v/env": "from-env", "v/file": "from-file", "v/absolute": "from-file", "v/home": "from-home"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("secrets = %v, want %v", got, want)
		}
	})

	t.Run("only secrets that are injected are read", func(t *testing.T) {
		p := project(map[string]string{"v/used": "env:EGZO_TEST_KEY", "v/unused": "env:EGZO_TEST_MISSING"})
		delete(p.Egress["default"].Services, "v/unused")
		if _, err := ResolveSecrets(p, dir); err != nil {
			t.Errorf("an unreferenced missing secret failed the run: %v", err)
		}
	})

	t.Run("reports every unreadable secret", func(t *testing.T) {
		_, err := ResolveSecrets(project(map[string]string{
			"v/unset":       "env:EGZO_TEST_MISSING",
			"v/empty-env":   "env:EGZO_TEST_EMPTY",
			"v/no-file":     "file:nope.txt",
			"v/empty-file":  "file:empty.txt",
			"v/odd-scheme":  "sops:x",
			"v/no-location": "",
		}), dir)
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, fragment := range []string{
			"secret v/unset: environment variable EGZO_TEST_MISSING is not set",
			"secret v/empty-env: environment variable EGZO_TEST_EMPTY is not set",
			"secret v/no-file: cannot read",
			"secret v/empty-file: " + filepath.Join(dir, "empty.txt") + " is empty",
			`secret v/odd-scheme: unsupported source "sops:x"`,
			"secret v/no-location: unsupported source",
		} {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("error does not contain %q:\n%v", fragment, err)
			}
		}
	})

	t.Run("a project with nothing to inject needs nothing", func(t *testing.T) {
		got, err := ResolveSecrets(project(nil), dir)
		if err != nil || len(got) != 0 {
			t.Errorf("ResolveSecrets = %v, %v", got, err)
		}
	})
}
