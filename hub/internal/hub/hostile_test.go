package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// The proxy writes its trail by rules (proxy.Event.Check): its hosts are host names, its texts are clipped. An engine the hub does
// not control can write any line in the proxy's log: what the proxy never writes is not kept, it is counted, and the Network view
// a person loads stays small whatever the log holds.
func TestEventsTheProxyNeverWritesAreNotKeptAndTheNetworkViewStaysSmall(t *testing.T) {
	r := newRig(t)
	r.link("web", "coder")
	op := r.operator()
	if err := r.hub.Spawn(ctx, op, r.id("web"), "coder", "coder-1"); err != nil {
		t.Fatal(err)
	}
	a := trailOf(r.agentNamed("web", "coder-1"))
	for i := range 600 {
		r.fakes["web"].AddAudit(a.denied(fmt.Sprintf("%d.%s", i, strings.Repeat("x", 32<<10)), proxy.CodeNotInProfile))
	}
	r.fakes["web"].AddAudit(a.denied("registry.npmjs.org", proxy.CodeNotInProfile))
	eventually(t, "the trail to be read", func() bool { return groups(r.networkOf(op, ""))["registry.npmjs.org not-in-profile"].Count == 1 })

	view := r.networkOf(op, "")
	for _, d := range view.Denials {
		if len(d.Host) > 253 {
			t.Errorf("a denial of a %d byte host is kept: no DNS name is longer than 253 bytes", len(d.Host))
			break
		}
	}
	if view.Ignored != 600 || !strings.Contains(view.IgnoredWhy, "host") {
		t.Errorf("ignored %d events (%s), want the 600 whose host no name has", view.Ignored, view.IgnoredWhy)
	}
	data, _ := json.Marshal(view)
	if len(data) > 2<<20 {
		t.Errorf("the Network view of one project is %d MiB, all of it chosen by an agent", len(data)>>20)
	}
}

// The budget of a feed is in bytes, and has to leave room for what is real: the proxy's log read back in one go when a link connects. This is
// a backlog of lines near their longest; a quiet link has a second's worth to spend at once, so it goes through in about a second.
func TestABacklogOfTheProxysLogFitsTheBudget(t *testing.T) {
	event := links.AuditEvent{Time: time.Now(), Conn: "c-0123456789abcdef", Kind: proxy.KindConnect, Decision: proxy.OutcomeError, Agent: "coder-1",
		Instance: "01JABCDEFGHJKMNPQRSTVWXYZ0", Profile: "default", UserAgent: strings.Repeat("u", 256), Host: strings.Repeat("h", 253),
		Path: strings.Repeat("p", 1024), Reason: &proxy.Reason{Code: proxy.CodeNotInProfile, Text: strings.Repeat("r", 512)}}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	backlog := links.AuditBacklog * len(line)
	t.Logf("one line of %d bytes, %d lines: %d KiB, in a budget of %d KiB a second", len(line), links.AuditBacklog, backlog>>10, bytesPerSecond>>10)
	if backlog > 2*bytesPerSecond {
		t.Errorf("a backlog of %d KiB is held back", backlog>>10)
	}
}
