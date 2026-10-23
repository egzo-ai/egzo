package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/proxy"
)

// An agent chooses the host of every connection it asks the proxy for, and the proxy writes it in its audit trail as it was asked,
// so what the hub reads of the trail is in part the agent's words. A host no DNS name can be (longer than 253 bytes) is junk, and the
// Network view a person loads stays small whatever an agent asked for.
func TestHostsNoNameCanBeAreNotKeptAndTheNetworkViewStaysSmall(t *testing.T) {
	r := newRig(t, "local")
	r.fakes["local"].AddProject(project("web", "coder"))
	op := r.operator()
	if _, err := r.hub.Spawn(ctx, op, "local", "web", "coder", ""); err != nil {
		t.Fatal(err)
	}
	r.startFeeds(flushing)
	r.scan()
	a := trailOf(r.agentNamed("web", "coder-1"))
	for i := range 600 {
		r.fakes["local"].AddAudit("web", a.denied(fmt.Sprintf("%d.%s", i, strings.Repeat("x", 32<<10)), proxy.CodeNotInProfile))
	}
	r.fakes["local"].AddAudit("web", a.denied("registry.npmjs.org", proxy.CodeNotInProfile))
	eventually(t, "the trail to be read", func() bool { return groups(r.networkOf(op, ""))["registry.npmjs.org not-in-profile"].Count == 1 })

	view := r.networkOf(op, "")
	for _, d := range view.Denials {
		if len(d.Host) > 253 {
			t.Errorf("a denial of a %d byte host is kept: no DNS name is longer than 253 bytes", len(d.Host))
			break
		}
	}
	data, _ := json.Marshal(view)
	if len(data) > 2<<20 {
		t.Errorf("the Network view of one project is %d MiB, all of it chosen by an agent", len(data)>>20)
	}
}
