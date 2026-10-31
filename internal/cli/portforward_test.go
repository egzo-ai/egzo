// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"io"
	"net"
	"testing"
	"time"
)

// pair is the two ends of a TCP connection: hanging up is about what the kernel does with unread bytes.
func pair(t *testing.T) (client, accepted *net.TCPConn) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dialed, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dialed.Close() })
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return dialed.(*net.TCPConn), conn.(*net.TCPConn)
}

func TestHangingUpOnAClientStillSendingLeavesItTheAnswer(t *testing.T) {
	client, accepted := pair(t)
	if _, err := client.Write(make([]byte, 1<<16)); err != nil { // unread by us: a plain close would reset
		t.Fatal(err)
	}
	if _, err := accepted.Write([]byte("refused")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() { hangUp(accepted); close(done) }()
	if _, err := client.Write(make([]byte, 1<<16)); err != nil {
		t.Fatalf("the client was cut off while sending: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer, err := io.ReadAll(client)
	if err != nil || string(answer) != "refused" {
		t.Errorf("got %q, %v", answer, err)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for a client that left")
	}
}

func TestHangingUpDoesNotWaitForeverForAQuietClient(t *testing.T) {
	hangUpTimeout = 50 * time.Millisecond
	defer func() { hangUpTimeout = 5 * time.Second }()
	_, accepted := pair(t)
	done := make(chan struct{})
	go func() { hangUp(accepted); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting")
	}
}
