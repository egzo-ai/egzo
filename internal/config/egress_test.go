// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"reflect"
	"strings"
	"testing"
)

const vaultFixture = `
vaults:
  main:
    backend: env
    secrets: [KEY, OTHER]
`

func TestEgressBuiltinServices(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      anthropic: main/KEY
      github: main/OTHER
`)
	services := resolved.Egress["default"].Services

	anthropic := services["anthropic"]
	if !reflect.DeepEqual(anthropic.Hosts, []string{"api.anthropic.com"}) || anthropic.Secret != "main/KEY" {
		t.Errorf("anthropic = %+v", anthropic)
	}
	if want := (&Inject{Header: "x-api-key"}); *anthropic.Inject != *want {
		t.Errorf("anthropic inject = %+v", anthropic.Inject)
	}
	github := services["github"]
	if github.Inject.Value != "Bearer {secret}" || github.Secret != "main/OTHER" {
		t.Errorf("github = %+v", github)
	}
}

func TestEgressBindingASecretDoesNotMutateTheBuiltin(t *testing.T) {
	mustResolve(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      anthropic: main/KEY\n")
	if builtinServices["anthropic"].Secret != "" {
		t.Error("the shared built-in service picked up a secret")
	}
	copied, _ := builtinService("anthropic")
	copied.Hosts[0] = "evil.example"
	copied.Inject.Header = "evil"
	if builtinServices["anthropic"].Hosts[0] != "api.anthropic.com" || builtinServices["anthropic"].Inject.Header != "x-api-key" {
		t.Error("builtinService returned a copy that aliases the original")
	}
}

func TestEgressUnknownService(t *testing.T) {
	wantProblems(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      anthropc: main/KEY\n",
		`unknown service "anthropc" (did you mean "anthropic"?)`)
	wantProblems(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      kubernetes: main/KEY\n",
		`unknown service "kubernetes" (built-in services: anthropic, anthropic-oauth, github)`)
}

func TestEgressEmptyServiceEntryIsRejected(t *testing.T) {
	wantProblems(t, t.TempDir(), "egress:\n  default:\n    services:\n      anthropic:\n", "invalid secret reference")
}

func TestEgressSecretReferences(t *testing.T) {
	cases := []struct{ name, ref, want string }{
		{"no slash", "KEY", "invalid secret reference"},
		{"a secret with a slash the vault does not list", "main/KEY/x", `does not list "KEY/x"`},
		{"empty vault", "/KEY", "invalid secret reference"},
		{"empty secret", "main/", "invalid secret reference"},
		{"unknown vault", "ghost/KEY", `unknown vault "ghost"`},
		{"unknown secret", "main/NOPE", `does not list "NOPE"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblems(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      anthropic: "+c.ref+"\n", c.want)
		})
	}
}

func TestEgressCustomServices(t *testing.T) {
	t.Run("definition with injection", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      internal:
        hosts: [api.internal.example]
        inject: { header: X-Token, value: "tok {secret}" }
        secret: main/KEY
        inspect: true
`)
		got := resolved.Egress["default"].Services["internal"]
		want := &ResolvedService{
			Hosts:   []string{"api.internal.example"},
			Inject:  &Inject{Header: "X-Token", Value: "tok {secret}"},
			Secret:  "main/KEY",
			Inspect: true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("service = %+v, want %+v", got, want)
		}
	})

	t.Run("a definition replaces the built-in of the same name", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), "egress:\n  default:\n    services:\n      github: { hosts: [ghe.example] }\n")
		got := resolved.Egress["default"].Services["github"]
		if !reflect.DeepEqual(got.Hosts, []string{"ghe.example"}) || got.Inject != nil {
			t.Errorf("github = %+v", got)
		}
	})

	cases := []struct{ name, body, want string }{
		{"no hosts", "{ inspect: true }", "needs hosts"},
		{"wildcard host", "{ hosts: ['*.example.com'] }", "invalid host"},
		{"host with scheme", "{ hosts: ['https://example.com'] }", "invalid host"},
		{"inject without header", "{ hosts: [a.com], inject: {}, secret: main/KEY }", "inject needs a header"},
		{"secret without inject", "{ hosts: [a.com], secret: main/KEY }", "neither inject nor placeholder"},
		{"bad secret", "{ hosts: [a.com], inject: { header: H }, secret: main/NOPE }", `does not list "NOPE"`},
		{"inject without any secret", "{ hosts: [a.com], inject: { header: H } }", "needs a secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblems(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      svc: "+c.body+"\n", c.want)
		})
	}
}

func TestEgressAllowEntries(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), "egress:\n  default:\n    allow: [a.com, '*.b.com', a.com, '*']\n")
	if want := []string{"a.com", "*.b.com", "*"}; !reflect.DeepEqual(resolved.Egress["default"].Allow, want) {
		t.Errorf("allow = %v, want deduplicated %v", resolved.Egress["default"].Allow, want)
	}
	wantProblems(t, t.TempDir(), "egress:\n  default:\n    allow: ['http://a.com', 'ok.com']\n", `invalid allow entry "http://a.com"`)
}

func TestEgressExtend(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  base:
    allow: [pypi.org]
    services:
      anthropic: main/KEY
  child:
    extend: base
    allow: [npmjs.org, pypi.org]
    services:
      github: main/OTHER
  rebind:
    extend: base
    services:
      anthropic: main/OTHER
`)
	child := resolved.Egress["child"]
	if want := []string{"pypi.org", "npmjs.org"}; !reflect.DeepEqual(child.Allow, want) {
		t.Errorf("child allow = %v, want %v", child.Allow, want)
	}
	if child.Services["anthropic"].Secret != "main/KEY" || child.Services["github"].Secret != "main/OTHER" {
		t.Errorf("child services = %+v", child.Services)
	}
	if _, leaked := resolved.Egress["base"].Services["github"]; leaked {
		t.Error("the child's service leaked into its parent")
	}
	if resolved.Egress["rebind"].Services["anthropic"].Secret != "main/OTHER" {
		t.Error("a child could not rebind an inherited service's secret")
	}
	if resolved.Egress["base"].Services["anthropic"].Secret != "main/KEY" {
		t.Error("rebinding in a child changed the parent")
	}
}

func TestEgressExtendDefault(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), "egress:\n  default: { allow: [a.com] }\n  tight: { extend: default, allow: [b.com] }\n")
	if want := []string{"a.com", "b.com"}; !reflect.DeepEqual(resolved.Egress["tight"].Allow, want) {
		t.Errorf("allow = %v, want %v", resolved.Egress["tight"].Allow, want)
	}
	// Extending an undefined default is fine: it is empty.
	mustResolve(t, t.TempDir(), "egress:\n  tight: { extend: default, allow: [b.com] }\n")
}

func TestEgressExtendErrors(t *testing.T) {
	wantProblems(t, t.TempDir(), "egress:\n  a: { extend: ghost }\n", `extends unknown profile "ghost"`)
	wantProblems(t, t.TempDir(), "egress:\n  a: { extend: b }\n  b: { extend: c }\n  c: { extend: a }\n", "extend cycle")
	wantProblems(t, t.TempDir(), "egress:\n  a: { extend: a }\n", "a -> a")
}

func TestEgressCycleReportNamesTheLoop(t *testing.T) {
	_, _, err := resolve(t, t.TempDir(), "egress:\n  a: { extend: b }\n  b: { extend: a }\n")
	if err == nil || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Errorf("error = %v", err)
	}
}

func TestEgressConflictingInjectionOnOneHost(t *testing.T) {
	wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      one: { hosts: [api.example.com], inject: { header: A }, secret: main/KEY }
      two: { hosts: [api.example.com], inject: { header: B }, secret: main/KEY }
`, `both cover host "api.example.com" with different injection`)

	// The same injection on the same host is not a conflict.
	mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      one: { hosts: [api.example.com], inject: { header: A }, secret: main/KEY }
      two: { hosts: [api.example.com], inject: { header: A }, secret: main/KEY }
`)
	// Nor are different hosts.
	mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      one: { hosts: [a.example.com], inject: { header: A }, secret: main/KEY }
      two: { hosts: [b.example.com], inject: { header: B }, secret: main/OTHER }
`)
}

func TestSameInjection(t *testing.T) {
	h := func(header string) *Inject { return &Inject{Header: header} }
	cases := []struct {
		name string
		a, b *ResolvedService
		want bool
	}{
		{"both plain", &ResolvedService{}, &ResolvedService{}, true},
		{"one plain", &ResolvedService{}, &ResolvedService{Inject: h("A")}, false},
		{"same header and secret", &ResolvedService{Inject: h("A"), Secret: "v/s"}, &ResolvedService{Inject: h("A"), Secret: "v/s"}, true},
		{"different header", &ResolvedService{Inject: h("A")}, &ResolvedService{Inject: h("B")}, false},
		{"different secret", &ResolvedService{Inject: h("A"), Secret: "v/1"}, &ResolvedService{Inject: h("A"), Secret: "v/2"}, false},
	}
	for _, c := range cases {
		if got := sameInjection(c.a, c.b); got != c.want {
			t.Errorf("%s: sameInjection = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProfileReaches(t *testing.T) {
	profile := &ResolvedProfile{
		Allow:    []string{"*.pypi.org", "exact.example"},
		Services: map[string]*ResolvedService{"svc": {Hosts: []string{"service.example"}}},
	}
	for host, want := range map[string]bool{
		"files.pypi.org":  true,
		"pypi.org":        false,
		"exact.example":   true,
		"service.example": true,
		"other.example":   false,
	} {
		if got := profile.reaches(host); got != want {
			t.Errorf("reaches(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestValidHost(t *testing.T) {
	for host, want := range map[string]bool{
		"example.com": true, "a-b.c": true, "localhost": true,
		"": false, "a b": false, "a/b": false, "a:80": false, "*.a": false, "http://a": false, "a\tb": false,
	} {
		if got := validHost(host); got != want {
			t.Errorf("validHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestBuiltinsAreSortedAndComplete(t *testing.T) {
	if got, want := builtinNames(), []string{"anthropic", "anthropic-oauth", "github"}; !reflect.DeepEqual(got, want) {
		t.Errorf("builtinNames = %v, want %v", got, want)
	}
	if got, want := HarnessNames(), []string{"claude-code", "custom", "opencode", "pi"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HarnessNames = %v, want %v", got, want)
	}
	if _, ok := builtinService("nope"); ok {
		t.Error("builtinService found a service that does not exist")
	}
}

func TestTheAnthropicOAuthServiceIsBuiltInAndSendsABearerToken(t *testing.T) {
	service, ok := builtinService("anthropic-oauth")
	if !ok || service.Hosts[0] != "api.anthropic.com" || service.Inject.Header != "Authorization" || service.Inject.Value != "Bearer {secret}" {
		t.Errorf("service = %+v, ok = %v", service, ok)
	}
}

func TestPlaceholderServices(t *testing.T) {
	const login = `hosts: [app.example.com], secret: main/KEY, placeholder: SITE_PASSWORD`

	t.Run("a placeholder service resolves and prints the variable", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      login: { "+login+" }\n")
		service := resolved.Egress["default"].Services["login"]
		if service.Placeholder != "SITE_PASSWORD" || service.Secret != "main/KEY" || service.Inject != nil {
			t.Errorf("service = %+v", service)
		}
	})

	t.Run("a child string keeps the placeholder", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  base: { services: { login: { `+login+` } } }
  child: { extend: base, services: { login: main/OTHER } }
`)
		service := resolved.Egress["child"].Services["login"]
		if service.Placeholder != "SITE_PASSWORD" || service.Secret != "main/OTHER" {
			t.Errorf("service = %+v", service)
		}
	})

	cases := []struct{ name, service, want string }{
		{"no secret", "{ hosts: [a.com], placeholder: SITE_PASSWORD }", `"login" has a placeholder but no secret`},
		{"inject and placeholder", "{ " + login + ", inject: { header: X } }", "both inject and placeholder"},
		{"secret with neither", "{ hosts: [a.com], secret: main/KEY }", `service "login" has a secret but neither inject nor placeholder`},
		{"dash in the name", "{ hosts: [a.com], secret: main/KEY, placeholder: MY-PASSWORD }", `"MY-PASSWORD"`},
		{"leading digit", "{ hosts: [a.com], secret: main/KEY, placeholder: 1PASSWORD }", `"1PASSWORD"`},
		{"set by egzo", "{ hosts: [a.com], secret: main/KEY, placeholder: HTTPS_PROXY }", `"HTTPS_PROXY"`},
		{"egzo prefix", "{ hosts: [a.com], secret: main/KEY, placeholder: EGZO_TOKEN }", `"EGZO_TOKEN"`},
		{"home", "{ hosts: [a.com], secret: main/KEY, placeholder: HOME }", `"HOME"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblems(t, t.TempDir(), vaultFixture+"egress:\n  default:\n    services:\n      login: "+c.service+"\n", c.want)
		})
	}

	t.Run("two services cannot share a variable", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      login: { `+login+` }
      other: { hosts: [b.example.com], secret: main/OTHER, placeholder: SITE_PASSWORD }
`, "SITE_PASSWORD")
	})

	t.Run("an agent cannot set a placeholder variable", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      login: { `+login+` }
agents:
  coder: { harness: custom, image: x, env: { SITE_PASSWORD: hunter2 } }
`, "SITE_PASSWORD")
	})

	t.Run("rules are by host only", func(t *testing.T) {
		for _, key := range []string{"path", "paths", "method", "methods"} {
			if _, err := Parse([]byte("egress:\n  default:\n    services:\n      login: { " + login + ", " + key + ": [x] }\n")); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s: err = %v", key, err)
			}
		}
	})
}
