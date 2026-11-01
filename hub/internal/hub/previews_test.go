// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/links/fake"
	"github.com/egzo-ai/egzo/hub/internal/preview"
	"github.com/egzo-ai/egzo/internal/hublink"
)

const previewID = "abcdefghijklmnop"

func linkedPreview(t *testing.T) (*rig, *fake.Engine, string) {
	t.Helper()
	r := newRig(t)
	f := r.link("web", "coder")
	f.SetAgent(links.Agent{Name: "coder-1", State: "running"})
	f.Expose("coder-1", links.Exposure{Port: 3000, Protocol: hublink.ProtocolHTTP, Where: hublink.WhereHub, ID: previewID, Name: "shop"})
	id := r.id("web")
	eventually(t, "the exposure to be heard", func() bool {
		_, err := r.hub.Previews().Find(ctx, id, previewID)
		return err == nil
	})
	return r, f, id
}

func TestThePreviewAddressIsGivenOnlyToAnApprovedProject(t *testing.T) {
	r := newRig(t)
	f := r.link("web", "coder")
	if got := last(f).Preview; got != "" {
		t.Errorf("a hub that serves no previews gave %q", got)
	}
	r.hub.ServePreviews("https", "egzo-preview.example.net")
	f.Drop()
	eventually(t, "the link to be gone", func() bool { return r.hub.connected(r.id("web")) == nil })
	r.connect(f)
	if got, want := last(f).Preview, "https://%s-"+r.id("web")+".egzo-preview.example.net/"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	stranger := fake.New("other", "laptop", "coder")
	r.turnedAway(stranger, "")
	if got := last(stranger).Preview; got != "" {
		t.Errorf("a link that is not approved heard %q", got)
	}
}

func TestAPreviewIsFoundByItsProject(t *testing.T) {
	r, f, id := linkedPreview(t)
	backend := r.hub.Previews()
	target, err := backend.Find(ctx, id, previewID)
	if err != nil || target != (preview.Target{Project: id, ID: previewID, Agent: "coder-1", Port: 3000}) {
		t.Fatalf("%+v %v", target, err)
	}
	if _, err := backend.Find(ctx, "p000000000000", previewID); !errors.Is(err, preview.ErrNotFound) {
		t.Errorf("another project: %v", err)
	}
	if _, err := backend.Find(ctx, id, "qqqqqqqqqqqqqqqq"); !errors.Is(err, preview.ErrNotFound) {
		t.Errorf("another id: %v", err)
	}
	f.Unexpose("coder-1", 3000)
	eventually(t, "the exposure to end", func() bool {
		_, err := backend.Find(ctx, id, previewID)
		return errors.Is(err, preview.ErrNotFound)
	})
}

func TestATCPExposureIsNoPreview(t *testing.T) {
	r, f, id := linkedPreview(t)
	f.Expose("coder-1", links.Exposure{Port: 5432, Protocol: hublink.ProtocolTCP, Where: hublink.WhereHub, ID: "qqqqqqqqqqqqqqqq"})
	eventually(t, "the line to be heard", func() bool {
		r.hub.feeds.mu.Lock()
		defer r.hub.feeds.mu.Unlock()
		_, ok := r.hub.feeds.exposed[id]["qqqqqqqqqqqqqqqq"]
		return ok
	})
	if _, err := r.hub.Previews().Find(ctx, id, "qqqqqqqqqqqqqqqq"); !errors.Is(err, preview.ErrNotFound) {
		t.Error(err)
	}
}

func TestWhatALinkSaysOfALocalPortIsChecked(t *testing.T) {
	for _, tc := range []struct {
		protocol, address string
		kept              bool
	}{
		{hublink.ProtocolHTTP, "http://127.0.0.1:20417/", true},
		{hublink.ProtocolHTTP, "http://mybox.lan:20417/", true},
		{hublink.ProtocolHTTP, "http://[fd00::1]:20417/", true},
		{hublink.ProtocolTCP, "127.0.0.1:20417", true},
		{hublink.ProtocolUDP, "mybox.lan:20417", true},
		{hublink.ProtocolHTTP, "127.0.0.1:20417", false},
		{hublink.ProtocolHTTP, "https://example.com:443/", false},
		{hublink.ProtocolHTTP, "http://127.0.0.1:20417/path", false},
		{hublink.ProtocolHTTP, "javascript:alert(1)//", false},
		{hublink.ProtocolTCP, "http://127.0.0.1:20417/", false},
		{hublink.ProtocolTCP, "127.0.0.1", false},
		{hublink.ProtocolTCP, "127.0.0.1:0", false},
		{hublink.ProtocolTCP, "127.0.0.1:70000", false},
		{hublink.ProtocolTCP, ":20417", false},
		{hublink.ProtocolTCP, "a b:20417", false},
		{hublink.ProtocolTCP, "<img>:20417", false},
		{hublink.ProtocolTCP, "", false},
		{"sctp", "127.0.0.1:20417", false},
	} {
		got := cleanExposures([]links.Exposure{{Port: 5432, Name: "db", Protocol: tc.protocol, Where: hublink.WhereLocal, ID: "llllllllllllllll", Address: tc.address}})
		if (len(got) == 1) != tc.kept {
			t.Errorf("%s %q: kept %+v", tc.protocol, tc.address, got)
		}
		if len(got) == 1 && got[0].ID != "" {
			t.Errorf("%q kept an id", tc.address)
		}
	}
	hub := cleanExposures([]links.Exposure{{Port: 3000, Protocol: hublink.ProtocolHTTP, Where: hublink.WhereHub, ID: "aaaaaaaaaaaaaaaa", Address: "http://evil.example/"}})
	if len(hub) != 1 || hub[0].Address != "" {
		t.Errorf("the hub makes the address of a preview itself: %+v", hub)
	}
}

func TestALocalPortIsListedAndItsAddressGoesToWhoMayOpenAPreview(t *testing.T) {
	r, f, id := linkedPreview(t)
	f.Expose("coder-1", links.Exposure{Port: 5432, Name: "db", Protocol: hublink.ProtocolTCP, Where: hublink.WhereLocal, Address: "127.0.0.1:20417"})
	find := func(maySee bool) *PreviewView {
		for _, view := range r.hub.previewsOf(id, "coder-1", maySee) {
			if view.Where == hublink.WhereLocal {
				return &view
			}
		}
		return nil
	}
	eventually(t, "the local port to be listed", func() bool { return find(true) != nil })
	if got := find(true); got.Address != "127.0.0.1:20417" || got.Name != "db" || got.Protocol != hublink.ProtocolTCP {
		t.Errorf("%+v", got)
	}
	if got := find(false); got == nil || got.Address != "" {
		t.Errorf("a person with no role was given %+v", got)
	}
}

func TestAPortOpenForTheLocalMachineIsNeverServed(t *testing.T) {
	r, f, id := linkedPreview(t)
	local := links.Exposure{Port: 8080, Protocol: hublink.ProtocolHTTP, Where: hublink.WhereLocal, ID: "llllllllllllllll", Name: "dev"}
	if got := exposedIn(links.StatusLine{Agents: []links.AgentStatus{{Agent: "coder-1", Exposed: []links.Exposure{local}}}}); len(got) != 0 {
		t.Errorf("exposedIn kept %+v", got)
	}
	f.Expose("coder-1", local)
	eventually(t, "the line to be heard", func() bool {
		r.hub.feeds.mu.Lock()
		defer r.hub.feeds.mu.Unlock()
		return len(r.hub.feeds.exposed[id]) == 1 // the preview of linkedPreview, and nothing else
	})
	if _, err := r.hub.Previews().Find(ctx, id, local.ID); !errors.Is(err, preview.ErrNotFound) {
		t.Error(err)
	}
}

func TestAnOfflineProjectSaysSo(t *testing.T) {
	r, f, id := linkedPreview(t)
	f.Drop()
	eventually(t, "the link to be gone", func() bool { return r.hub.connected(id) == nil })
	if _, err := r.hub.Previews().Find(ctx, id, previewID); !errors.Is(err, preview.ErrOffline) {
		t.Error(err)
	}
	if _, err := r.hub.Previews().Dial(ctx, preview.Target{Project: id, Agent: "coder-1", Port: 3000}); !errors.Is(err, preview.ErrOffline) {
		t.Error(err)
	}
}

func TestAConnectionOutlivesTheDialAndEndsWithTheLink(t *testing.T) {
	r, f, id := linkedPreview(t)
	f.Listen("coder-1", 3000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	dialing, cancel := context.WithCancel(ctx)
	conn, err := r.hub.Previews().Dial(dialing, preview.Target{Project: id, Agent: "coder-1", Port: 3000})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel() // what http.Transport does once a dial is done
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("the dial's context ended the connection: %v", err)
	}
	buffer := make([]byte, 512)
	n, err := conn.Read(buffer)
	if err != nil || !strings.Contains(string(buffer[:n]), "200 OK") {
		t.Fatalf("%q %v", buffer[:n], err)
	}
	if err := r.hub.Revoke(ctx, r.operator(), id, "enough"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(conn)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the preview outlived the project")
	}
}

func TestWhatALinkSaysOfItsPortsIsCleaned(t *testing.T) {
	r, f, id := linkedPreview(t)
	long := strings.Repeat("x", maxWord+10)
	line := links.StatusLine{Agents: []links.AgentStatus{{Agent: "coder-1", Exposed: []links.Exposure{
		{Port: 0, Protocol: "http", Where: "hub", ID: previewID}, {Port: 70000, Protocol: "http", Where: "hub", ID: previewID}, {Port: 80, Protocol: "udp", Where: "hub", ID: previewID},
		{Port: 80, Protocol: "http", Where: "hub", ID: "../../etc"}, {Port: 80, Protocol: "http", Where: "hub", ID: "UPPERCASEUPPERCA"}, {Port: 8080, Protocol: "http", Where: "hub", ID: "aaaaaaaaaaaaaaaa", Name: "\x1b[2J" + long},
	}}}}
	f.PushStatus(line)
	eventually(t, "the line to be heard", func() bool {
		r.hub.feeds.mu.Lock()
		defer r.hub.feeds.mu.Unlock()
		return len(r.hub.feeds.exposed[id]) == 1
	})
	r.hub.feeds.mu.Lock()
	defer r.hub.feeds.mu.Unlock()
	if got := r.hub.feeds.exposed[id]["aaaaaaaaaaaaaaaa"]; got.Port != 8080 || len(got.Name) > maxWord+3 || strings.ContainsRune(got.Name, 0x1b) {
		t.Errorf("%+v", got)
	}
}

func TestTooManyPortsAreCut(t *testing.T) {
	list := make([]links.Exposure, 0, maxExposures+5)
	for i := range maxExposures + 5 {
		list = append(list, links.Exposure{Port: 1000 + i, Protocol: "http", Where: "hub", ID: previewID})
	}
	if got := cleanExposures(list); len(got) != maxExposures {
		t.Errorf("%d", len(got))
	}
}
