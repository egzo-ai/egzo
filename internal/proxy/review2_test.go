// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// A deadline set from outside, such as net/http aborting a pending read with one in the past, must never
// be overwritten by a Read that loaded the old limit just before.
func TestAnAbortedPendingReadIsAlwaysAbortedPromptly(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := newIdleConn(server, time.Hour)
	for i := 0; i < 500; i++ {
		read := make(chan error, 1)
		go func() {
			_, err := conn.Read(make([]byte, 1))
			read <- err
		}()
		if i%2 == 0 {
			time.Sleep(time.Duration(i%7) * 20 * time.Microsecond)
		}
		conn.SetReadDeadline(time.Unix(1, 0))
		select {
		case err := <-read:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("round %d: read = %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: the pending read was not aborted", i)
		}
		conn.SetReadDeadline(time.Time{})
	}
}

func TestAnAbortedPendingWriteIsAlwaysAbortedPromptly(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := newIdleConn(server, time.Hour)
	for i := 0; i < 200; i++ {
		write := make(chan error, 1)
		go func() {
			_, err := conn.Write([]byte("x")) // nobody reads: it blocks
			write <- err
		}()
		if i%2 == 0 {
			time.Sleep(time.Duration(i%7) * 20 * time.Microsecond)
		}
		conn.SetWriteDeadline(time.Unix(1, 0))
		select {
		case err := <-write:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("round %d: write = %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: the pending write was not aborted", i)
		}
		conn.SetWriteDeadline(time.Time{})
	}
}

func TestAMalformedHostInsideATerminatedConnectionIsAudited(t *testing.T) {
	r := newRig(t, nil)
	long := strings.Repeat("a", 5000) + "!x"
	if status := rawRequestThroughTermination(t, r, "GET / HTTP/1.1\r\nHost: "+long+"\r\nConnection: close\r\n\r\n"); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	waitFor(t, func() bool { return r.hasStep("example.com", "request/deny") })
	log := r.audit.String()
	if !strings.Contains(log, "malformed Host header") || strings.Contains(log, strings.Repeat("a", 400)) {
		t.Errorf("the denial is missing, or the Host was not clipped:\n%.600s", log)
	}
}

func TestAnIPv6LiteralHostHeaderMatchesItsConnectTarget(t *testing.T) {
	// net.SplitHostPort gives the CONNECT target without brackets.
	host, _, _ := net.SplitHostPort("[2001::1]:443")
	if got, ok := requestHost("[2001::1]:443"); !ok || got != normalizeHost(host) {
		t.Errorf("the Host header names %q, the CONNECT target %q", got, host)
	}
}

func TestTimeoutsLeftAtZeroUseTheDefaults(t *testing.T) {
	r := newRig(t, nil)
	r.proxy.IdleTimeout, r.proxy.HandshakeTimeout = 0, 0
	if status := rawRequestThroughTermination(t, r, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
}

func TestATokenLongerThanAnyRealOneIsRefused(t *testing.T) {
	r := newRig(t, nil)
	long := strings.Repeat("t", maxToken+1)
	r.setPolicy("coder", long, []string{"example.com"})
	if first(r.proxy.authenticate(r.proxy.policy.Load(), "coder", long)) != nil {
		t.Error("a token over the limit was accepted")
	}
}
