package config

import "sort"

// builtinServices ship with egzo as data. A profile can bind a secret to one by name, or replace
// it entirely with its own definition of the same name.
var builtinServices = map[string]ResolvedService{
	"anthropic": {
		Hosts:  []string{"api.anthropic.com"},
		Inject: &Inject{Header: "x-api-key"},
	},
	// A Claude subscription token (`claude setup-token`, sk-ant-oat...) is not an API key: the API takes it
	// as a bearer token. Bind it to this service, and an API key to "anthropic".
	"anthropic-oauth": {
		Hosts:  []string{"api.anthropic.com"},
		Inject: &Inject{Header: "Authorization", Value: "Bearer {secret}"},
	},
	"github": {
		Hosts:  []string{"github.com", "api.github.com"},
		Inject: &Inject{Header: "Authorization", Value: "Bearer {secret}"},
	},
}

func builtinService(name string) (*ResolvedService, bool) {
	service, ok := builtinServices[name]
	if !ok {
		return nil, false
	}
	copied := service
	copied.Hosts = append([]string(nil), service.Hosts...)
	if service.Inject != nil {
		inject := *service.Inject
		copied.Inject = &inject
	}
	return &copied, true
}

func builtinNames() []string {
	names := make([]string, 0, len(builtinServices))
	for name := range builtinServices {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// harnesses lists the supported harnesses and the provider hosts each one needs to reach.
var harnesses = map[string][]string{
	"claude-code": {"api.anthropic.com"},
	"opencode":    {"models.opencode.ai"},
	"pi":          nil,
	"custom":      nil,
}

// HarnessNames lists the supported harnesses, sorted.
func HarnessNames() []string {
	names := make([]string, 0, len(harnesses))
	for name := range harnesses {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
