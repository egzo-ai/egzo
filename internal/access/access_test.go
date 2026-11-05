// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package access

import (
	"strings"
	"testing"
)

func sample() Access {
	return Access{
		Profile: "team", Allow: []string{"example.com"}, OtherHosts: OtherHosts,
		Services: []Service{
			{Name: "github", Hosts: []string{"github.com", "api.github.com"}, Credential: Injected, How: How("github", Injected, "")},
			{Name: "site", Hosts: []string{"httpbin.org"}, Credential: Placeholder, Variable: "SITE_PASSWORD", How: How("site", Placeholder, "SITE_PASSWORD")},
		},
		SSH: []string{"prod"},
	}
}

func TestChangesSaysWhatWasAddedAndTakenAway(t *testing.T) {
	before := sample()
	after := sample()
	after.Allow = append(after.Allow, "pypi.org")
	after.SSH = nil
	after.Services = after.Services[:1]
	got := Changes(before, after)
	for _, want := range []string{"hosts now allowed: pypi.org", "ssh servers no longer allowed: prod", "services no longer allowed: site (httpbin.org)"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not say %q", got, want)
		}
	}
	if Changes(before, sample()) != "" {
		t.Error("nothing changed, nothing said")
	}
	other := sample()
	other.Services[1].Paths = []string{"/api/"}
	if Changes(before, other) == "" || Equal(before, other) {
		t.Error("a service whose paths changed is a change the agent is told of")
	}
}

func TestTextNamesEverythingAndNeverASecretReference(t *testing.T) {
	text := sample().Text()
	for _, want := range []string{"egress profile: team", "example.com", "github.com", "GH_TOKEN=x", "$SITE_PASSWORD", "ssh servers: prod", OtherHosts} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}
}
