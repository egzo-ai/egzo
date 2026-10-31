// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package portforward carries one connection to a port of an agent through a command run in its container. The command is
// `egzo agent forward PORT`: it connects to the container's own loopback and copies bytes between that socket and its stdin and
// stdout. Whoever holds the engine connection decides where the port shows up, so nothing is published and the sandbox does not
// change, and the command takes a port and never an address, so it reaches nothing else on the agent's network.
package portforward

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// dialTimeout bounds connecting to loopback, which refuses at once: only a server with a full accept queue takes any time.
const dialTimeout = 5 * time.Second

// drainTimeout is how long a server may stay silent once the client has ended its input. The client may only have ended its side
// (the server still answers), or be gone: a stream cannot tell the two apart, and a server that ignores the end must not keep the
// command running in the agent for ever.
var drainTimeout = time.Minute

// drainLimit is how long the way back stays open once the client has ended its input, however much the server says. A client that
// is gone is noticed by nobody here: the engine keeps reading the command's stdout after the attach is dropped, so a server that
// keeps talking (an event stream, a tail) would otherwise keep the command, and its connection, for ever. The price is that a client
// that really only ended its side gets at most this long of answer.
var drainLimit = 10 * time.Minute

// Command is the command that forwards one connection, run in the agent's container.
func Command(port int) []string {
	return []string{"egzo", "agent", "forward", strconv.Itoa(port)}
}

// NotAPort is what ParsePort says about a port it refuses.
const NotAPort = "is not a port: use a number from 1 to 65535"

// ParsePort reads a port number, 1 to 65535.
func ParsePort(text string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%q %s", text, NotAPort)
	}
	return port, nil
}

// Mapping is what `egzo agent port-forward` listens on and the agent's port it reaches.
type Mapping struct {
	Address string // default 127.0.0.1
	Local   int    // 0 lets the system choose
	Port    int
}

// Listen is the address to listen on.
func (m Mapping) Listen() string {
	return net.JoinHostPort(m.Address, strconv.Itoa(m.Local))
}

// ParseMapping reads [[ADDRESS:]LOCAL:]PORT. An IPv6 address goes in brackets.
func ParseMapping(text string) (Mapping, error) {
	m := Mapping{Address: "127.0.0.1"}
	rest, port, hasLocal := cutLast(text)
	if !hasLocal {
		port = text
	}
	var err error
	if m.Port, err = ParsePort(port); err != nil {
		return m, err
	}
	if !hasLocal {
		m.Local = m.Port
		return m, nil
	}
	address, local, hasAddress := cutLast(rest)
	if !hasAddress {
		local = rest
	}
	if local != "0" {
		if m.Local, err = ParsePort(local); err != nil {
			return m, fmt.Errorf("local port: %w", err)
		}
	}
	if hasAddress {
		address = strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
		if _, err := netip.ParseAddr(address); err != nil && (address == "" || strings.Contains(address, ":")) {
			return m, fmt.Errorf("%q is not an address to listen on: use [[ADDRESS:]LOCAL:]PORT, an IPv6 address in brackets", address)
		}
		m.Address = address
	}
	return m, nil
}

func cutLast(text string) (before, after string, found bool) {
	if i := strings.LastIndexByte(text, ':'); i >= 0 {
		return text[:i], text[i+1:], true
	}
	return text, "", false
}

// ErrNothingListens says the port is closed on both loopback addresses.
type ErrNothingListens struct{ Port int }

func (e ErrNothingListens) Error() string { return fmt.Sprintf("nothing listens on port %d", e.Port) }

// dial connects to a port of this machine's loopback, the IPv4 address first: dev servers bind one or the other.
func dial(port int) (*net.TCPConn, error) {
	var failure error
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialTimeout)
		if err == nil {
			return conn.(*net.TCPConn), nil
		}
		if failure == nil && !unreachable(err) {
			failure = err
		}
	}
	if failure != nil {
		return nil, failure
	}
	return nil, ErrNothingListens{Port: port}
}

// unreachable reports a refusal, or a container without that loopback address or that address family at all.
func unreachable(err error) bool {
	for _, reason := range []error{syscall.ECONNREFUSED, syscall.EADDRNOTAVAIL, syscall.ENETUNREACH, syscall.EAFNOSUPPORT, syscall.EPROTONOSUPPORT} {
		if errors.Is(err, reason) {
			return true
		}
	}
	return false
}

// Forward copies bytes between in and out and a port of this machine's loopback, until the server closes. The end of in shuts down
// the socket's write side and leaves the way back open, for as long as the server has something to say (drainTimeout), up to
// drainLimit.
func Forward(port int, in io.Reader, out io.Writer) error {
	conn, err := dial(port)
	if err != nil {
		return err
	}
	defer conn.Close()
	drain, limit := drainTimeout, drainLimit
	var inputEnded atomic.Bool
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.CloseWrite()
		inputEnded.Store(true)
		_ = conn.SetReadDeadline(time.Now().Add(drain))
		time.AfterFunc(limit, func() { _ = conn.Close() })
	}()
	buffer := make([]byte, 32<<10)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				return nil // the client is gone
			}
			if inputEnded.Load() {
				_ = conn.SetReadDeadline(time.Now().Add(drain))
			}
		}
		if err != nil {
			var timeout net.Error
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) || errors.As(err, &timeout) && timeout.Timeout() {
				return nil
			}
			return err
		}
	}
}
