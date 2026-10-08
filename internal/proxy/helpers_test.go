// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/x509"
	"testing"
	"time"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition never held")
	}
}

func certPool(ca *CA) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// testUpstreamCA is the pool that trusts the rig's upstream server.
func testUpstreamCA(r *rig) *CA { return &CA{Cert: r.upstream.Certificate()} }
