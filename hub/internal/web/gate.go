package web

import (
	"net/netip"
	"sync"
	"time"
)

// The link endpoint takes anyone. maxHandshakes keeps the hub's own cost in bounds, but on its own it hands the slots to whoever holds them
// longest, and a project reconnecting gets a 503. So one address gets a small share of them, and an address that keeps failing is told to
// wait. Only attempts count: a project behind the same address as a hundred others, registered and connected, costs nothing here, and a
// link that is not registered stops dialing after its first answer.
const (
	// maxProving is how many handshakes one address may have going at once. A NAT full of projects that all came back goes on retrying.
	maxProving = 8
	// maxFailures is how many failed attempts an address gets in a failureWindow: a proof that does not verify, an unknown key, a bad code.
	// A code is 128 random bits, so this is about the work a guess costs the hub, not about the guess.
	maxFailures   = 20
	failureWindow = time.Minute
	// maxClients is how many addresses are remembered. Past it, what is not in a handshake is dropped.
	maxClients = 1 << 16
)

type gateClient struct {
	proving  int
	failures int
	since    time.Time // when the window of its failures began
	logged   time.Time // when a failure of it was last written to the log
}

// linkGate says what one address may try on the link endpoint.
type linkGate struct {
	mu      sync.Mutex
	clients map[netip.Addr]*gateClient
	now     func() time.Time
}

func newLinkGate() *linkGate {
	return &linkGate{clients: map[netip.Addr]*gateClient{}, now: time.Now}
}

// clientKey is what counts as one address: the machine for IPv4, the network for IPv6, where a machine holds a whole /64.
func clientKey(addr netip.Addr) netip.Addr {
	if addr.Is6() {
		if prefix, err := addr.Prefix(64); err == nil {
			return prefix.Addr()
		}
	}
	return addr
}

func (g *linkGate) client(addr netip.Addr) *gateClient {
	key := clientKey(addr)
	c := g.clients[key]
	if c == nil {
		if len(g.clients) >= maxClients {
			g.prune()
		}
		c = &gateClient{}
		g.clients[key] = c
	}
	if g.now().Sub(c.since) >= failureWindow {
		c.failures, c.since = 0, g.now()
	}
	return c
}

// prune forgets the addresses that are not in a handshake: those whose window is over first, and all of them if that is not enough.
func (g *linkGate) prune() {
	for key, c := range g.clients {
		if c.proving == 0 && g.now().Sub(c.since) >= failureWindow {
			delete(g.clients, key)
		}
	}
	if len(g.clients) >= maxClients {
		for key, c := range g.clients {
			if c.proving == 0 {
				delete(g.clients, key)
			}
		}
	}
}

// enter asks to begin a handshake; leave says it is over, however it ended.
func (g *linkGate) enter(addr netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.client(addr)
	if c.proving >= maxProving || c.failures >= maxFailures {
		return false
	}
	c.proving++
	return true
}

func (g *linkGate) leave(addr netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c := g.clients[clientKey(addr)]; c != nil && c.proving > 0 {
		c.proving--
	}
}

// failed counts a failed attempt, and says whether to write it to the log: once a window, so that a stranger cannot fill it.
func (g *linkGate) failed(addr netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.client(addr)
	c.failures++
	if g.now().Sub(c.logged) < failureWindow {
		return false
	}
	c.logged = g.now()
	return true
}
