package proxy

import "testing"

func TestDecideWildcardServiceAuditsAndExactServiceInjects(t *testing.T) {
	// The wildcard service comes first: it must not take the name, and must not stop the exact one injecting.
	profile := Profile{Services: []Service{
		{Name: "browse", Hosts: []string{"*"}, Inspect: true},
		{Name: "anthropic", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Secret: "k"},
	}}
	got := profile.Decide("api.anthropic.com")
	if !got.Allowed || got.Service != "anthropic" || !got.Inspect || got.Inject == nil || got.Inject.Value != "k" {
		t.Errorf("exact host decision = %+v", got)
	}
	got = profile.Decide("other.example.com")
	if !got.Allowed || got.Service != "browse" || !got.Inspect || got.Inject != nil {
		t.Errorf("covered host decision = %+v", got)
	}
	if got := (&Profile{Services: []Service{{Name: "w", Hosts: []string{"*.example.com"}}}}).Decide("example.com"); got.Allowed {
		t.Errorf("*.example.com covered the bare domain: %+v", got)
	}
}
