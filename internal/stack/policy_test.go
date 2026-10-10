// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"slices"

	"github.com/egzo-ai/egzo/internal/engine"
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
	policy := BuildPolicy(policyProject(), map[string]string{"main/KEY": "s3cret"}, nil)

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
	policy := BuildPolicy(project, nil, nil)
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
	policy := BuildPolicy(project, nil, nil)
	profile := policy.Profiles["default"]
	profile.Allow[0] = "mutated.example"
	if project.Egress["default"].Allow[0] != "pypi.org" {
		t.Error("mutating the policy changed the resolved profile")
	}
}

func TestBuildPolicyHash(t *testing.T) {
	secrets := map[string]string{"main/KEY": "one"}
	base := BuildPolicy(policyProject(), secrets, nil)

	if base.Hash == "" || len(base.Hash) != 64 {
		t.Fatalf("hash = %q, want a sha256 hex digest", base.Hash)
	}
	if again := BuildPolicy(policyProject(), secrets, nil); again.Hash != base.Hash {
		t.Error("the hash is not deterministic")
	}
	if changed := BuildPolicy(policyProject(), map[string]string{"main/KEY": "two"}, nil); changed.Hash == base.Hash {
		t.Error("a new secret value did not change the hash")
	}
}

func TestResolveSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EGZO_TEST_KEY", "from-env")
	t.Setenv("EGZO_TEST_EMPTY", "")

	project := func(names ...string) *config.Resolved {
		services := map[string]*config.ResolvedService{}
		secrets := map[string]config.SecretRef{}
		for _, name := range names {
			ref := "v/" + name
			services[ref] = &config.ResolvedService{Inject: &config.Inject{Header: "H"}, Secret: ref}
			secrets[ref] = config.SecretRef{Vault: "v", Name: name, Backend: "env"}
		}
		return &config.Resolved{
			Secrets: secrets,
			Agents:  map[string]config.ResolvedAgent{"a": {Egress: "default"}},
			Egress:  map[string]*config.ResolvedProfile{"default": {Services: services}},
		}
	}

	t.Run("reads the environment backend", func(t *testing.T) {
		got, err := ResolveSecrets(project("EGZO_TEST_KEY"), dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]string{"v/EGZO_TEST_KEY": "from-env"}; !reflect.DeepEqual(got, want) {
			t.Errorf("secrets = %v, want %v", got, want)
		}
	})

	t.Run("only secrets that are injected are read", func(t *testing.T) {
		p := project("EGZO_TEST_KEY", "EGZO_TEST_MISSING")
		delete(p.Egress["default"].Services, "v/EGZO_TEST_MISSING")
		if _, err := ResolveSecrets(p, dir); err != nil {
			t.Errorf("an unreferenced missing secret failed the run: %v", err)
		}
	})

	t.Run("reports every unreadable secret", func(t *testing.T) {
		p := project("EGZO_TEST_MISSING", "EGZO_TEST_EMPTY", "viapass")
		p.Secrets["v/viapass"] = config.SecretRef{Vault: "v", Name: "viapass", Backend: "pass"}
		t.Setenv("PATH", t.TempDir())
		_, err := ResolveSecrets(p, dir)
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, fragment := range []string{
			"secret v/EGZO_TEST_MISSING: environment variable EGZO_TEST_MISSING is not set",
			"secret v/EGZO_TEST_EMPTY: environment variable EGZO_TEST_EMPTY is not set",
			"secret v/viapass: `pass` was not found on PATH",
		} {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("error %q does not contain %q", err, fragment)
			}
		}
	})
}

func placeholderProject() *config.Resolved {
	return &config.Resolved{
		Agents: map[string]config.ResolvedAgent{"coder": {Egress: "default"}},
		Egress: map[string]*config.ResolvedProfile{
			"default": {Services: map[string]*config.ResolvedService{
				"tokens": {Hosts: []string{"api.example"}, Secret: "main/TOK", Placeholder: "API_TOKEN"},
			}},
		},
	}
}

func TestBuildPolicyForAPlaceholderService(t *testing.T) {
	ph := map[string]string{"main/TOK": "egzo-ph-aaaa"}
	policy := BuildPolicy(placeholderProject(), map[string]string{"main/TOK": "real"}, ph)
	got := policy.Profiles["default"].Services[0]
	want := proxy.Service{Name: "tokens", Hosts: []string{"api.example"}, Secret: "real", Placeholder: "egzo-ph-aaaa"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("service = %+v, want %+v", got, want)
	}
	rotated := BuildPolicy(placeholderProject(), map[string]string{"main/TOK": "other"}, ph)
	if rotated.Hash == policy.Hash {
		t.Error("a rotated secret leaves the hash alone")
	}
	other := BuildPolicy(placeholderProject(), map[string]string{"main/TOK": "real"}, map[string]string{"main/TOK": "egzo-ph-bbbb"})
	if other.Hash == policy.Hash {
		t.Error("another placeholder leaves the hash alone")
	}
}

func TestPlaceholdersReachTheTemplateAndTheEnvironmentNotTheSecret(t *testing.T) {
	project := placeholderProject()
	ph := map[string]string{"main/TOK": "egzo-ph-0123456789abcdef0123456789abcdef"}
	published, err := PublishWith(project, t.TempDir(), "egzo:test", "", "", ph)
	if err != nil {
		t.Fatal(err)
	}
	template := published.Templates["coder"]
	if !reflect.DeepEqual(template.Placeholders, map[string]string{"API_TOKEN": ph["main/TOK"]}) {
		t.Fatalf("placeholders = %v", template.Placeholders)
	}
	again, _ := PublishWith(project, t.TempDir(), "egzo:test", "", "", nil)
	if again.Templates["coder"].Hash != template.Hash {
		t.Error("the placeholder text moves the hash: an instance would go stale for a text that never changes")
	}
	view := published.View(map[string]string{"coder-1": "coder"})
	spec, err := agentContainer(view, "", "coder-1", view.Agents["coder-1"], "net", published.inputs(map[string]string{"coder-1": "coder"}, map[string]string{"coder-1": "tok"}, nil), engine.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Env, "API_TOKEN="+ph["main/TOK"]) {
		t.Errorf("env lacks the placeholder: %v", spec.Env)
	}
}

func TestPlaceholderRefsAndStandIns(t *testing.T) {
	refs := PlaceholderRefs(placeholderProject())
	if !reflect.DeepEqual(refs, []string{"main/TOK"}) {
		t.Fatalf("refs = %v", refs)
	}
	if got := withStandIns(refs, nil)["main/TOK"]; got != placeholderStandIn {
		t.Errorf("stand-in = %q", got)
	}
	if len(placeholderStandIn) != len("egzo-ph-")+32 {
		t.Errorf("stand-in %q has the wrong shape", placeholderStandIn)
	}
}
