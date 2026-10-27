// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"slices"
	"sort"
)

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
	// The key of an OpenCode Go or Console workspace (a service account's) is a bearer token for opencode.ai, which is
	// also the website and the Console. The paths are where OpenCode sends it (anomalyco/opencode, packages/core/src/plugin/
	// provider/opencode.ts): the model calls (Zen and Go under /inference/, /zen/ before them), and the Console's config
	// (the workspace's header for the model calls, its models and policies) and web search. Its model list, on
	// models.opencode.ai, takes none.
	"opencode": {
		Hosts:  []string{"opencode.ai"},
		Paths:  []string{"/inference/", "/zen/", "/console/api/v2/config", "/console/api/websearch"},
		Inject: &Inject{Header: "Authorization", Value: "Bearer {secret}"},
	},
	// git over HTTPS refuses a bearer token; GitHub takes the token as the password of a Basic credential,
	// which its API accepts too. This is only what a custom service can say.
	"github": {
		Hosts:  []string{"github.com", "api.github.com"},
		Inject: &Inject{Header: "Authorization", BasicUser: "x-access-token"},
	},
	// An OpenAI API key, sent as a bearer token (platform.openai.com/docs/api-reference/authentication). Codex reads it from
	// its auth.json, where egzo writes the placeholder.
	"openai": {
		Hosts:  []string{"api.openai.com"},
		Inject: &Inject{Header: "Authorization", Value: "Bearer {secret}"},
	},
	// A ChatGPT Plus or Pro sign-in, as Codex makes it (openai/codex, login/src/auth/manager.rs and revoke.rs). chatgpt.com
	// is also ChatGPT's website: Codex's calls go under /backend-api/ (the model, usage, apps), and nothing else gets the token.
	"chatgpt": {
		Hosts: []string{"chatgpt.com"},
		Paths: []string{"/backend-api/"},
		Credential: &Credential{
			Method:    MethodOAuth,
			TokenURL:  "https://auth.openai.com/oauth/token",
			RevokeURL: "https://auth.openai.com/oauth/revoke",
		},
	},
}

func builtinService(name string) (*ResolvedService, bool) {
	service, ok := builtinServices[name]
	if !ok {
		return nil, false
	}
	copied := service
	copied.Hosts = append([]string(nil), service.Hosts...)
	copied.Paths = slices.Clone(service.Paths)
	if service.Inject != nil {
		inject := *service.Inject
		copied.Inject = &inject
	}
	if service.Credential != nil {
		credential := *service.Credential
		copied.Credential = &credential
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
	"codex":       nil, // chatgpt.com for a ChatGPT sign-in or api.openai.com for an API key: see codexReach
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
