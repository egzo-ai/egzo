// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPrivateHostMayBeDialedAtAPrivateAddressButNeverALocalOne(t *testing.T) {
	up, err := newUpstream(Upstream{PrivateHosts: []string{"idp.internal.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := notLocal("tcp", "10.1.2.3:443", nil); err != nil {
		t.Errorf("a private address of a private host: %v", err)
	}
	for _, address := range []string{"127.0.0.1:443", "169.254.169.254:443", "[::1]:443", "0.0.0.0:443"} {
		if notLocal("tcp", address, nil) == nil {
			t.Errorf("%s was allowed", address)
		}
	}
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer listener.Close()
	if conn, err := up.dial(context.Background(), "tcp", listener.Addr().String()); err == nil {
		conn.Close()
		t.Error("an address that is no private host was dialed at a loopback address")
	}
}

func TestTheUpstreamCAIsTrustedForInterceptedHosts(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "internal") }))
	defer backend.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: backend.Certificate().Raw}))
	server := NewServer(mustCA(t), NewAudit(io.Discard))
	get := func() error {
		request, _ := http.NewRequest("GET", backend.URL, nil)
		response, err := server.transport().RoundTrip(request)
		if err == nil {
			response.Body.Close()
		}
		return err
	}
	server.upstream.Load().transport.DialContext = (&net.Dialer{}).DialContext // the backend listens on loopback
	if err := get(); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an unknown CA was trusted: %v", err)
	}
	if err := server.SetPolicy(&Policy{Hash: "h", Upstream: &Upstream{CA: ca}}); err != nil {
		t.Fatal(err)
	}
	server.upstream.Load().transport.DialContext = (&net.Dialer{}).DialContext
	if err := get(); err != nil {
		t.Errorf("the upstream CA is not trusted: %v", err)
	}
	if server.SetPolicy(&Policy{Hash: "h2", Upstream: &Upstream{CA: "not pem"}}) == nil {
		t.Error("an upstream CA without a certificate was accepted")
	}
}
