// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"syscall"
	"time"
)

// Upstream is what the proxy trusts and may dial on the way out, besides the public internet and the system roots
// (proxy.upstream in egzo.yaml). It travels with the policy.
type Upstream struct {
	// CA is PEM certificates added to the system roots for the TLS the proxy makes to an intercepted host.
	CA string `json:"ca,omitempty"`
	// PrivateHosts are the exact host names the proxy may reach at a private address.
	PrivateHosts []string `json:"private_hosts,omitempty"`
}

// upstream is how the proxy reaches the outside under one Upstream: the transport for the hosts it intercepts and the
// dial for the ones it tunnels.
type upstream struct {
	config    Upstream
	transport *http.Transport
	dial      func(ctx context.Context, network, address string) (net.Conn, error)
}

func newUpstream(config Upstream) (*upstream, error) {
	var roots *x509.CertPool
	if config.CA != "" {
		system, err := x509.SystemCertPool()
		if err != nil {
			system = x509.NewCertPool()
		}
		if !system.AppendCertsFromPEM([]byte(config.CA)) {
			return nil, errors.New("the upstream CA holds no certificate")
		}
		roots = system
	}
	public := &net.Dialer{Timeout: dialTimeout, Control: publicOnly}
	private := &net.Dialer{Timeout: dialTimeout, Control: notLocal}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err == nil && slices.Contains(config.PrivateHosts, host) {
			return private.DialContext(ctx, network, address)
		}
		return public.DialContext(ctx, network, address)
	}
	return &upstream{
		config: config,
		transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			DialContext:           dial,
			TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   defaultHandshakeTimeout,
			ResponseHeaderTimeout: 5 * time.Minute,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		},
		dial: dial,
	}, nil
}

// notLocal is the dial check for a private host: a private address is what it is for, but never loopback, a link-local
// address such as a cloud metadata endpoint, or an unspecified or multicast one.
func notLocal(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%s is a local address", host)
	}
	return nil
}

// setUpstream switches to the upstream of a policy, when it differs from the one in use.
func (s *Server) setUpstream(config Upstream) error {
	current := s.upstream.Load()
	if current != nil && current.config.CA == config.CA && slices.Equal(current.config.PrivateHosts, config.PrivateHosts) {
		return nil
	}
	next, err := newUpstream(config)
	if err != nil {
		return err
	}
	s.upstream.Store(next)
	if current != nil {
		current.transport.CloseIdleConnections()
	}
	return nil
}

// transport reaches the upstream of an intercepted host.
func (s *Server) transport() http.RoundTripper {
	if s.Transport != nil {
		return s.Transport
	}
	return s.upstream.Load().transport
}

// dial reaches the upstream of a tunnelled host.
func (s *Server) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if s.Dial != nil {
		return s.Dial(ctx, network, address)
	}
	return s.upstream.Load().dial(ctx, network, address)
}
