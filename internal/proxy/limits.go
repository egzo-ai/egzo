package proxy

import (
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// Defaults for what one agent, or all of them together, may hold open at the proxy, and how long a
// connection may stay silent. The proxy is shared by every agent of the project, so a compromised
// agent must not be able to use up what the others need.
const (
	defaultMaxPerAgent = 256
	defaultMaxTotal    = 2048
	defaultIdleTimeout = 10 * time.Minute
)

// limiter counts open connections per agent and in all.
type limiter struct {
	mu       sync.Mutex
	perAgent map[string]int
	total    int
}

// acquire takes a slot for agent, or says why there is none. release gives it back.
func (l *limiter) acquire(agent string, maxPerAgent, maxTotal int) (release func(), reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perAgent == nil {
		l.perAgent = map[string]int{}
	}
	switch {
	case l.perAgent[agent] >= maxPerAgent:
		return nil, fmt.Sprintf("too many connections: agent %s already holds %d", agent, maxPerAgent)
	case l.total >= maxTotal:
		return nil, fmt.Sprintf("too many connections: the proxy already holds %d", maxTotal)
	}
	l.perAgent[agent]++
	l.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.perAgent[agent]--; l.perAgent[agent] <= 0 {
				delete(l.perAgent, agent)
			}
			l.total--
		})
	}, ""
}

// idleConn gives every read and write its own deadline, so a connection that goes quiet is closed
// while one that keeps moving data is not.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c idleConn) Read(p []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c idleConn) Write(p []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// copyIdle copies src to dst with an idle timeout on both sides, and reports how many bytes moved.
func copyIdle(dst, src net.Conn, idle time.Duration) int64 {
	n, _ := io.Copy(idleConn{dst, idle}, idleConn{src, idle})
	return n
}

// publicRanges are the address blocks that are not part of the public internet and not caught by the
// net.IP predicates.
var publicRanges = func() []*net.IPNet {
	var blocks []*net.IPNet
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b:1::/48", "2001:db8::/32"} {
		_, block, _ := net.ParseCIDR(cidr)
		blocks = append(blocks, block)
	}
	return blocks
}()

// isPublic reports whether ip is an address on the public internet. The proxy sits on every agent's
// network, so it must never be a way to reach loopback, a private network, link-local services such as
// a cloud metadata endpoint, or another agent.
func isPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range publicRanges {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// publicOnly is a net.Dialer Control function: it runs on the address a name resolved to, just before
// connecting, so it also covers a name whose DNS answer points inside.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if !isPublic(net.ParseIP(host)) {
		return fmt.Errorf("%s is not a public address", host)
	}
	return nil
}
