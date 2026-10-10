package config

import (
	"reflect"
	"testing"
)

func TestEgressWildcardServices(t *testing.T) {
	t.Run("a wildcard service without a secret is accepted and normalized", func(t *testing.T) {
		resolved, _ := mustResolve(t, t.TempDir(), "egress:\n  default:\n    services:\n      docs: { hosts: ['*.Example.COM.', 'Docs.example.org'], inspect: true }\n      all: { hosts: ['*'] }\n")
		services := resolved.Egress["default"].Services
		if want := []string{"*.example.com", "docs.example.org"}; !reflect.DeepEqual(services["docs"].Hosts, want) || !services["docs"].Inspect {
			t.Errorf("docs = %+v, want hosts %v", services["docs"], want)
		}
		if !reflect.DeepEqual(services["all"].Hosts, []string{"*"}) {
			t.Errorf("all = %+v", services["all"])
		}
	})

	t.Run("a wildcard service next to an exact injecting one is accepted", func(t *testing.T) {
		mustResolve(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      browse: { hosts: ['*'], inspect: true }
      anthropic: main/KEY
`)
	})

	t.Run("a wildcard service defined by a child is refused when it holds a secret", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default: {}
  child:
    extend: default
    services:
      deploy-api: { hosts: ['*.example.com'], inject: { header: H }, secret: main/KEY }
`, `service "deploy-api"`, "exact", `"*.example.com"`)
	})

	t.Run("a child replacing a service with a wildcard secret one is refused", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      svc: { hosts: [a.com], inject: { header: H }, secret: main/KEY }
  child:
    extend: default
    services:
      svc: { hosts: ['*'], inject: { header: H }, secret: main/KEY }
`, `service "svc"`, "exact")
	})

	t.Run("a secret cannot be bound to an inherited wildcard service", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      browse: { hosts: ['*'], inspect: true }
  child:
    extend: default
    services:
      browse: main/KEY
`, `service "browse"`, "inject", "placeholder")
	})

	t.Run("two services still cannot share an exact host with different injection", func(t *testing.T) {
		wantProblems(t, t.TempDir(), vaultFixture+`
egress:
  default:
    services:
      a: { hosts: [x.com], inject: { header: A }, secret: main/KEY }
      b: { hosts: [x.com], inject: { header: B }, secret: main/KEY }
`, "both cover host")
	})
}
