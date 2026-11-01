// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package relay

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// start runs a relay on loopback whose connections all go to upstream, with the gate answering from open.
func start(t *testing.T, port Port, upstream string, open *atomic.Bool, adjust func(*Options)) *Relay {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := Options{
		Listen: net.ParseIP("127.0.0.1"), Target: "agent", Ports: []Port{port},
		Gate: func(ctx context.Context, name string) (bool, error) { return open.Load() && name == port.Name, nil },
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upstream)
		},
	}
	if adjust != nil {
		adjust(&opts)
	}
	r, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func allow() *atomic.Bool { var b atomic.Bool; b.Store(true); return &b }

// echoThenCount answers each byte it reads, and at the end of the input says how many it read.
func echoThenCount(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				n, _ := io.Copy(c, c)
				io.WriteString(c, "\nread "+strings.Repeat("x", 0)+itoa(n))
			}()
		}
	}()
	return l.Addr().String()
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestATCPConnectionIsCarriedBothWaysAndAHalfCloseStillGetsItsAnswer(t *testing.T) {
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, echoThenCount(t), allow(), nil)
	c, err := net.Dial("tcp", r.Addr("db").String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "hello")
	c.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "hello\nread 5" {
		t.Errorf("%q, %v", got, err)
	}
}

func TestAnHTTPPortIsCarriedAsTCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") }))
	defer server.Close()
	r := start(t, Port{Name: "web", Protocol: ProtocolHTTP}, strings.TrimPrefix(server.URL, "http://"), allow(), nil)
	response, err := http.Get("http://" + r.Addr("web").String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); string(body) != "hi" {
		t.Errorf("%q", body)
	}
}

func TestAClosedGateRefusesNewConnectionsAndLeavesOpenOnesAlone(t *testing.T) {
	open := allow()
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, echoThenCount(t), open, nil)
	first, err := net.Dial("tcp", r.Addr("db").String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	io.WriteString(first, "a")
	buf := make([]byte, 1)
	first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(first, buf); err != nil {
		t.Fatal(err)
	}
	open.Store(false)
	second, err := net.Dial("tcp", r.Addr("db").String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(second, "b")
	if n, err := second.Read(buf); n != 0 || err == nil {
		t.Errorf("a refused connection carried %d bytes", n)
	}
	io.WriteString(first, "c")
	first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(first, buf); err != nil || buf[0] != 'c' {
		t.Errorf("the open connection was cut: %q, %v", buf, err)
	}
}

func TestAGateThatDoesNotAnswerIsARefusal(t *testing.T) {
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, echoThenCount(t), allow(), func(o *Options) {
		o.Gate = func(ctx context.Context, name string) (bool, error) { return false, context.DeadlineExceeded }
	})
	c, err := net.Dial("tcp", r.Addr("db").String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "x")
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Error("a connection went through without an answer from the gate")
	}
}

func TestOnlyAPortOfTheListIsOpenedForItsOwnName(t *testing.T) {
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, echoThenCount(t), allow(), func(o *Options) {
		o.Gate = func(ctx context.Context, name string) (bool, error) { return name == "other", nil }
	})
	c, _ := net.Dial("tcp", r.Addr("db").String())
	defer c.Close()
	io.WriteString(c, "x")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _ := c.Read(make([]byte, 1)); n != 0 {
		t.Error("a port went through on the gate's yes for another")
	}
}

func TestTheNumberOfTCPConnectionsOfAPortIsBounded(t *testing.T) {
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, echoThenCount(t), allow(), func(o *Options) { o.MaxConnections = 2 })
	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for range 2 {
		c, err := net.Dial("tcp", r.Addr("db").String())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		io.WriteString(c, "x")
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
	}
	third, err := net.Dial("tcp", r.Addr("db").String())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	third.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(third, "x")
	if n, _ := third.Read(make([]byte, 1)); n != 0 {
		t.Error("a third connection was carried")
	}
	held[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", r.Addr("db").String())
		if err == nil {
			io.WriteString(c, "x")
			c.SetReadDeadline(time.Now().Add(time.Second))
			n, _ := c.Read(make([]byte, 1))
			c.Close()
			if n == 1 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("a slot never came back")
		}
	}
}

func TestADialThatFailsEndsTheConnection(t *testing.T) {
	r := start(t, Port{Name: "db", Protocol: ProtocolTCP}, "127.0.0.1:1", allow(), nil)
	c, _ := net.Dial("tcp", r.Addr("db").String())
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("read from nothing")
	}
}

// udpEcho answers each datagram with the same bytes prefixed "re:", and tells each source address it saw.
func udpEcho(t *testing.T) (string, func() int) {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	var sources atomic.Int32
	seen := map[string]bool{}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if !seen[from.String()] {
				seen[from.String()] = true
				sources.Add(1)
			}
			c.WriteTo(append([]byte("re:"), buf[:n]...), from)
		}
	}()
	return c.LocalAddr().String(), func() int { return int(sources.Load()) }
}

func exchange(t *testing.T, c net.Conn, text string) string {
	t.Helper()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		return ""
	}
	return string(buf[:n])
}

func TestUDPSessionsAreOnePerClientAndRepliesGoToTheirOwnClient(t *testing.T) {
	address, sources := udpEcho(t)
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, address, allow(), nil)
	a, _ := net.Dial("udp", r.Addr("voice").String())
	b, _ := net.Dial("udp", r.Addr("voice").String())
	defer a.Close()
	defer b.Close()
	if got := exchange(t, a, "one"); got != "re:one" {
		t.Errorf("%q", got)
	}
	if got := exchange(t, b, "two"); got != "re:two" {
		t.Errorf("%q", got)
	}
	if got := exchange(t, a, "three"); got != "re:three" {
		t.Errorf("%q", got)
	}
	if sources() != 2 {
		t.Errorf("the agent saw %d sources, want one socket for each client", sources())
	}
}

func TestAClosedGateStopsNewUDPSessionsOnly(t *testing.T) {
	address, _ := udpEcho(t)
	open := allow()
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, address, open, nil)
	a, _ := net.Dial("udp", r.Addr("voice").String())
	defer a.Close()
	if exchange(t, a, "one") != "re:one" {
		t.Fatal("no answer")
	}
	open.Store(false)
	b, _ := net.Dial("udp", r.Addr("voice").String())
	defer b.Close()
	if got := exchange(t, b, "two"); got != "" {
		t.Errorf("a new session went through: %q", got)
	}
	if got := exchange(t, a, "three"); got != "re:three" {
		t.Errorf("the session that was open was cut: %q", got)
	}
}

func TestAnIdleUDPSessionEndsAndAnotherGetsAFreshSocket(t *testing.T) {
	address, sources := udpEcho(t)
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, address, allow(), func(o *Options) { o.UDPIdle = 150 * time.Millisecond })
	a, _ := net.Dial("udp", r.Addr("voice").String())
	defer a.Close()
	exchange(t, a, "one")
	time.Sleep(500 * time.Millisecond)
	exchange(t, a, "two")
	if sources() != 2 {
		t.Errorf("%d sockets to the agent, want a second after the idle timeout", sources())
	}
}

func TestTheNumberOfUDPSessionsOfAPortIsBounded(t *testing.T) {
	address, _ := udpEcho(t)
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, address, allow(), func(o *Options) { o.MaxConnections = 2 })
	var clients []net.Conn
	for range 3 {
		c, _ := net.Dial("udp", r.Addr("voice").String())
		clients = append(clients, c)
		defer c.Close()
	}
	answered := 0
	for _, c := range clients {
		if exchange(t, c, "x") != "" {
			answered++
		}
	}
	if answered != 2 {
		t.Errorf("%d sessions were carried, want 2", answered)
	}
}

func TestADatagramThatDoesNotFitIsDropped(t *testing.T) {
	address, _ := udpEcho(t)
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, address, allow(), nil)
	c, _ := net.Dial("udp", r.Addr("voice").String())
	defer c.Close()
	if got := exchange(t, c, strings.Repeat("x", MaxDatagram+100)); got != "" {
		t.Errorf("carried %d bytes", len(got))
	}
	if got := exchange(t, c, "small"); got != "re:small" {
		t.Errorf("%q", got)
	}
}

func TestNothingTheAgentSendsStartsASession(t *testing.T) {
	agent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	r := start(t, Port{Name: "voice", Protocol: ProtocolUDP}, agent.LocalAddr().String(), allow(), nil)
	// the agent sends to the relay's port: that is a client like any other, and a client is let through only by the gate, to the agent's own port
	c, _ := net.Dial("udp", r.Addr("voice").String())
	defer c.Close()
	c.Write([]byte("hello"))
	agent.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, from, err := agent.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("%q %v", buf[:n], err)
	}
	// a datagram from the agent to the relay's upstream socket reaches the client who owns it, and no one else
	other, _ := net.Dial("udp", r.Addr("voice").String())
	defer other.Close()
	agent.WriteTo([]byte("reply"), from)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := c.Read(buf); err != nil || string(buf[:n]) != "reply" {
		t.Errorf("%q %v", buf[:n], err)
	}
	other.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := other.Read(buf); err == nil {
		t.Errorf("another client heard %q", buf[:n])
	}
}

func TestStartingFailsWhenAPortCannotBeOpened(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port
	_, err = Start(context.Background(), Options{Listen: net.ParseIP("127.0.0.1"), Target: "a", Ports: []Port{{Name: "a", Port: port, Protocol: ProtocolTCP}}, Gate: nil})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("%v", err)
	}
}

func TestTheListenAddressIsOneOfTheOutsideNetworkAndNeverTheAgents(t *testing.T) {
	_, outside, _ := net.ParseCIDR("172.20.0.0/16")
	inside := &net.IPNet{IP: net.ParseIP("172.30.0.5"), Mask: net.CIDRMask(16, 32)}
	mine := &net.IPNet{IP: net.ParseIP("172.20.0.7"), Mask: net.CIDRMask(16, 32)}
	if ip, ok := ListenAddress([]*net.IPNet{outside}, []net.Addr{inside, mine}); !ok || !ip.Equal(net.ParseIP("172.20.0.7")) {
		t.Errorf("%v %v", ip, ok)
	}
	if _, ok := ListenAddress([]*net.IPNet{outside}, []net.Addr{inside}); ok {
		t.Error("an address of the agent's network was taken")
	}
}

func TestTheControlGateAsksAsTheAgentAndOnlyAcceptsLocalForTheName(t *testing.T) {
	var user, password string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ = r.BasicAuth()
		if r.URL.Path != "/v1/expose" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `[{"name":"db","where":"local"},{"name":"web","where":"hub"}]`)
	}))
	defer server.Close()
	gate := ControlGate(server.Client(), server.URL, "coder-1", "token")
	for name, want := range map[string]bool{"db": true, "web": false, "nothing": false} {
		if got, err := gate(context.Background(), name); err != nil || got != want {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
	if user != "coder-1" || password != "token" {
		t.Errorf("asked as %q %q", user, password)
	}
	broken := ControlGate(server.Client(), server.URL+"/nope", "a", "b")
	if ok, err := broken(context.Background(), "db"); ok || err == nil {
		t.Error("a control sidecar that errs opened a port")
	}
	if ok, err := ControlGate(&http.Client{Timeout: 100 * time.Millisecond}, "http://127.0.0.1:1", "a", "b")(context.Background(), "db"); ok || err == nil {
		t.Error("a control sidecar that is not there opened a port")
	}
}
