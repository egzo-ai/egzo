package cli

import (
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/stack"
)

func TestPruneMatchesAnInstanceThatMatchesAnyFilterAndSaysWhy(t *testing.T) {
	now := time.Now()
	current := stack.Published{Templates: map[string]stack.Template{"coder": {Hash: "new"}}}
	instances := []stack.Instance{
		{Name: "running-fresh", Template: "coder", State: "running", TemplateHash: "new", Created: now.Add(-time.Minute)},
		{Name: "stopped", Template: "coder", State: "exited", TemplateHash: "new", Created: now.Add(-time.Minute)},
		{Name: "stale", Template: "coder", State: "running", TemplateHash: "old", Created: now.Add(-time.Minute)},
		{Name: "orphan", Template: "removed-template", State: "running", TemplateHash: "x", Created: now.Add(-time.Minute)},
		{Name: "old", Template: "coder", State: "running", TemplateHash: "new", Created: now.Add(-48 * time.Hour)},
		{Name: "all-three", Template: "coder", State: "exited", TemplateHash: "old", Created: now.Add(-48 * time.Hour)},
	}
	reasons := func(filters pruneFilters) map[string]string {
		out := map[string]string{}
		for _, c := range pruneCandidates(instances, filters, current, now) {
			out[c.Name] = c.Reason
		}
		return out
	}
	if got := reasons(pruneFilters{stopped: true}); len(got) != 2 || got["stopped"] != "stopped" || got["all-three"] != "stopped" {
		t.Errorf("--stopped = %v", got)
	}
	if got := reasons(pruneFilters{stale: true}); len(got) != 3 || got["stale"] != "stale" || got["orphan"] != "stale" || got["all-three"] != "stale" {
		t.Errorf("--stale = %v (an instance whose template is gone is stale)", got)
	}
	if got := reasons(pruneFilters{olderThan: 24 * time.Hour}); len(got) != 2 || got["old"] != "older than 24h0m0s" {
		t.Errorf("--older-than = %v", got)
	}
	if got := reasons(pruneFilters{stopped: true, stale: true, olderThan: 24 * time.Hour}); len(got) != 5 || got["all-three"] != "stopped, stale, older than 24h0m0s" {
		t.Errorf("all filters = %v", got)
	}
	if got := reasons(pruneFilters{}); len(got) != 0 {
		t.Errorf("no filter matched %v", got)
	}
}
