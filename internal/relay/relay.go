// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package relay carries connections from a port published on the machine to a port of an agent. It is the `egzo relay` role: one relay per
// instance, on a network of its own and on the agent's, holding no secret of the project, no engine socket and no volume.
//
// The relay listens on every port its instance declares, and lets a connection through only when the control sidecar says the agent has that
// port open for the local machine. It asks at each new TCP connection and each new UDP session, so closing a port stops new ones at once. A
// connection that is already open lasts until it ends, and a UDP session until it has been idle for a while.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// What a declared port speaks. `http` is carried as TCP: the relay does not read it.
const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
)

// The limits of a relay. They bound what a stranger on the network can make it hold.
const (
	// MaxConnections is how many TCP connections one port carries at once, and how many UDP sessions.
	MaxConnections = 64
	// DialTimeout is how long a connection waits for the agent's server.
	DialTimeout = 5 * time.Second
	// UDPIdle is how long a UDP session lives without a datagram either way.
	UDPIdle = 2 * time.Minute
	// MaxDatagram is the largest datagram carried: a bigger one is dropped.
	MaxDatagram = 16 << 10
	// GateTimeout is how long the control sidecar has to answer.
	GateTimeout = 3 * time.Second
	// queued is how many datagrams wait for a new session to be let through.
	queued = 16
)

// Port is one declared port of the instance.
type Port struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// Gate says whether a port is open for the local machine now. An error is a refusal.
type Gate func(ctx context.Context, name string) (bool, error)

// Options are what a relay runs with.
type Options struct {
	// Listen is the address the relay listens on, an IP of the network the outside reaches it through and never one of the agent's.
	Listen net.IP
	// Target is the agent's host name, resolved at each connection: the agent may not be up yet.
	Target string
	Ports  []Port
	Gate   Gate
	// Dial connects to the agent; net.Dialer.DialContext when nil. The tests replace it.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// MaxConnections, UDPIdle and DialTimeout replace the limits above, for the tests.
	MaxConnections int
	UDPIdle        time.Duration
	DialTimeout    time.Duration
	Log            *slog.Logger
}

func (o *Options) defaults() {
	if o.Dial == nil {
		o.Dial = (&net.Dialer{}).DialContext
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = MaxConnections
	}
	if o.UDPIdle == 0 {
		o.UDPIdle = UDPIdle
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = DialTimeout
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// Relay is a running relay: where each port listens.
type Relay struct {
	opts      Options
	cancel    context.CancelFunc
	listeners []io.Closer
	wg        sync.WaitGroup
	addrs     map[string]net.Addr
}

// Addr is where the named port listens.
func (r *Relay) Addr(name string) net.Addr { return r.addrs[name] }

// Start opens every port. A port that cannot be opened fails the start and closes the ones opened before it.
func Start(ctx context.Context, opts Options) (*Relay, error) {
	opts.defaults()
	ctx, cancel := context.WithCancel(ctx)
	r := &Relay{opts: opts, cancel: cancel, addrs: map[string]net.Addr{}}
	for _, port := range opts.Ports {
		address := net.JoinHostPort(opts.Listen.String(), strconv.Itoa(port.Port))
		var err error
		if port.Protocol == ProtocolUDP {
			err = r.startUDP(ctx, port, address)
		} else {
			err = r.startTCP(ctx, port, address)
		}
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("listen on %s for %s: %w", address, port.Name, err)
		}
	}
	return r, nil
}

// Close stops listening and ends what is open.
func (r *Relay) Close() {
	r.cancel()
	for _, l := range r.listeners {
		l.Close()
	}
	r.wg.Wait()
}

func (r *Relay) target(port Port) string {
	return net.JoinHostPort(r.opts.Target, strconv.Itoa(port.Port))
}

// open asks the gate, and fails closed.
func (r *Relay) open(ctx context.Context, port Port) bool {
	ctx, cancel := context.WithTimeout(ctx, GateTimeout)
	defer cancel()
	ok, err := r.opts.Gate(ctx, port.Name)
	if err != nil {
		r.opts.Log.Warn("the control sidecar did not answer: connection refused", "port", port.Name, "err", err)
		return false
	}
	return ok
}

func (r *Relay) startTCP(ctx context.Context, port Port, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	r.listeners = append(r.listeners, listener)
	r.addrs[port.Name] = listener.Addr()
	slots := make(chan struct{}, r.opts.MaxConnections)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close() // over the limit for this port
				continue
			}
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer func() { <-slots }()
				r.carry(ctx, port, conn)
			}()
		}
	}()
	return nil
}

// carry is one TCP connection: let through by the gate, then bytes both ways until both are done.
func (r *Relay) carry(ctx context.Context, port Port, client net.Conn) {
	defer client.Close()
	if !r.open(ctx, port) {
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, r.opts.DialTimeout)
	upstream, err := r.opts.Dial(dialCtx, "tcp", r.target(port))
	cancel()
	if err != nil {
		return
	}
	defer upstream.Close()
	// Closing both when the relay stops ends a copy that waits on a quiet peer.
	stop := context.AfterFunc(ctx, func() { client.Close(); upstream.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	go func() { halfCopy(upstream, client); done <- struct{}{} }()
	go func() { halfCopy(client, upstream); done <- struct{}{} }()
	<-done
	<-done
}

// halfCopy copies one way and, at its end, tells the other side that nothing more comes: a half close, so a client that has sent all it has
// still gets its answer.
func halfCopy(to, from net.Conn) {
	_, _ = io.Copy(to, from)
	if c, ok := to.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	} else {
		to.Close()
	}
}

// session is one client of a UDP port: a socket to the agent of its own.
type session struct {
	in       chan []byte
	activity atomic.Int64 // unix nanoseconds of the last datagram either way
	done     chan struct{}
}

func (r *Relay) startUDP(ctx context.Context, port Port, address string) error {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	r.listeners = append(r.listeners, conn)
	r.addrs[port.Name] = conn.LocalAddr()
	var mu sync.Mutex
	sessions := map[string]*session{}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		buffer := make([]byte, MaxDatagram+1)
		for {
			n, client, err := conn.ReadFrom(buffer)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			if n > MaxDatagram {
				continue // does not fit
			}
			key := client.String()
			mu.Lock()
			s := sessions[key]
			if s == nil {
				if len(sessions) >= r.opts.MaxConnections {
					mu.Unlock()
					continue
				}
				s = &session{in: make(chan []byte, queued), done: make(chan struct{})}
				s.activity.Store(time.Now().UnixNano())
				sessions[key] = s
				r.wg.Add(1)
				go func() {
					defer r.wg.Done()
					r.serveUDP(ctx, port, conn, client, s)
					mu.Lock()
					delete(sessions, key)
					mu.Unlock()
				}()
			}
			mu.Unlock()
			select {
			case s.in <- append([]byte(nil), buffer[:n]...):
			default: // a session that cannot keep up loses datagrams, as UDP does
			}
		}
	}()
	return nil
}

// serveUDP is one UDP session. The gate decides once, before the first datagram goes through. Replies come from the socket connected to the
// agent, so only the agent's port can answer, and they go to this client alone. The agent cannot start a session.
func (r *Relay) serveUDP(ctx context.Context, port Port, listener net.PacketConn, client net.Addr, s *session) {
	defer close(s.done)
	if !r.open(ctx, port) {
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, r.opts.DialTimeout)
	upstream, err := r.opts.Dial(dialCtx, "udp", r.target(port))
	cancel()
	if err != nil {
		return
	}
	defer upstream.Close()
	replies := make(chan struct{})
	go func() {
		defer close(replies)
		buffer := make([]byte, MaxDatagram+1)
		for {
			n, err := upstream.Read(buffer)
			if err != nil {
				return
			}
			if n > MaxDatagram {
				continue
			}
			s.activity.Store(time.Now().UnixNano())
			_, _ = listener.WriteTo(buffer[:n], client)
		}
	}()
	idle := time.NewTimer(r.opts.UDPIdle)
	defer idle.Stop()
	for {
		select {
		case datagram := <-s.in:
			s.activity.Store(time.Now().UnixNano())
			if _, err := upstream.Write(datagram); err != nil {
				return
			}
		case <-idle.C:
			since := time.Since(time.Unix(0, s.activity.Load()))
			if since >= r.opts.UDPIdle {
				return
			}
			idle.Reset(r.opts.UDPIdle - since)
		case <-replies:
			return
		case <-ctx.Done():
			return
		}
	}
}
