package web

import (
	"net/netip"
	"testing"
	"time"
)

func TestOneAddressGetsAShareOfTheHandshakesAndOthersGetTheirs(t *testing.T) {
	g := newLinkGate()
	busy, other := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	for range maxProving {
		if !g.enter(busy) {
			t.Fatal("an address was refused its share")
		}
	}
	if g.enter(busy) {
		t.Error("an address took more than its share")
	}
	if !g.enter(other) {
		t.Error("another address was refused")
	}
	g.leave(busy)
	if !g.enter(busy) {
		t.Error("a handshake that ended still counts")
	}
}

func TestAnAddressThatFailsWaitsAndComesBack(t *testing.T) {
	g := newLinkGate()
	now := time.Now()
	g.now = func() time.Time { return now }
	addr := netip.MustParseAddr("192.0.2.1")
	if !g.failed(addr) {
		t.Error("the first failure is not logged")
	}
	if g.failed(addr) {
		t.Error("a stranger can fill the log")
	}
	for range maxFailures {
		g.failed(addr)
	}
	if g.enter(addr) {
		t.Error("an address that keeps failing was let in")
	}
	now = now.Add(failureWindow)
	if !g.enter(addr) {
		t.Error("an address stayed locked out after its window")
	}
}

func TestAConnectedProjectCostsItsAddressNothing(t *testing.T) {
	g := newLinkGate()
	addr := netip.MustParseAddr("192.0.2.1")
	for range 10 * maxProving { // a NAT full of projects, each of which proved its key and stays connected
		if !g.enter(addr) {
			t.Fatal("projects behind one address were refused")
		}
		g.leave(addr)
	}
}

func TestANetworkOfIPv6IsOneAddress(t *testing.T) {
	if clientKey(netip.MustParseAddr("2001:db8:1:2:aaaa::1")) != clientKey(netip.MustParseAddr("2001:db8:1:2:bbbb::2")) {
		t.Error("a machine can walk its /64")
	}
	if clientKey(netip.MustParseAddr("2001:db8:1:2::1")) == clientKey(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Error("two networks are one")
	}
}

func TestTheGateForgetsWhatItCannotHold(t *testing.T) {
	g := newLinkGate()
	busy := netip.MustParseAddr("10.0.0.1")
	g.enter(busy)
	first := netip.MustParseAddr("10.0.0.2").As4()
	for i := range maxClients + 5 {
		first[1], first[2], first[3] = byte(i>>16), byte(i>>8), byte(i)
		g.failed(netip.AddrFrom4(first))
	}
	if len(g.clients) > maxClients {
		t.Errorf("%d addresses are held", len(g.clients))
	}
	if g.clients[busy] == nil || g.clients[busy].proving != 1 {
		t.Error("an address in a handshake was forgotten")
	}
}
