// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package portforward

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseMapping(t *testing.T) {
	cases := map[string]Mapping{
		"3000":                {"127.0.0.1", 3000, 3000},
		"8000:3000":           {"127.0.0.1", 8000, 3000},
		"0:3000":              {"127.0.0.1", 0, 3000},
		"0.0.0.0:8000:3000":   {"0.0.0.0", 8000, 3000},
		"localhost:8000:3000": {"localhost", 8000, 3000},
		"[::1]:8000:3000":     {"::1", 8000, 3000},
	}
	for text, want := range cases {
		if got, err := ParseMapping(text); err != nil || got != want {
			t.Errorf("%s = %+v, %v", text, got, err)
		}
	}
	for _, text := range []string{"", "http", "0", "65536", ":3000", "x:3000", "a:b:c", "1:2:3:4", ":8000:3000", "8000:0"} {
		if got, err := ParseMapping(text); err == nil {
			t.Errorf("%q was accepted: %+v", text, got)
		}
	}
}

func listen(t *testing.T, network, address string) (*net.TCPListener, int) {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Skipf("no %s here: %v", address, err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener.(*net.TCPListener), listener.Addr().(*net.TCPAddr).Port
}

func TestNothingListening(t *testing.T) {
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	listener.Close()
	var closed ErrNothingListens
	if err := Forward(port, strings.NewReader(""), io.Discard); !errors.As(err, &closed) || closed.Port != port {
		t.Errorf("got %v", err)
	}
}

func TestAServerOnIPv6LoopbackIsReached(t *testing.T) {
	listener, port := listen(t, "tcp6", "[::1]:0")
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Write([]byte("six"))
			conn.Close()
		}
	}()
	var out bytes.Buffer
	if err := Forward(port, blocked(t), &out); err != nil || out.String() != "six" {
		t.Errorf("got %q, %v", out.String(), err)
	}
}

// blocked is an input that stays open for the whole test.
func blocked(t *testing.T) io.Reader {
	reader, writer := io.Pipe()
	t.Cleanup(func() { writer.Close() })
	return reader
}

func TestTheEndOfInputLeavesTheAnswerComing(t *testing.T) {
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		got, _ := io.ReadAll(conn)
		conn.Write(append([]byte("read "), got...))
	}()
	var out bytes.Buffer
	if err := Forward(port, strings.NewReader("all of it"), &out); err != nil || out.String() != "read all of it" {
		t.Errorf("got %q, %v", out.String(), err)
	}
}

func TestAServerThatIgnoresTheEndOfInputIsLeft(t *testing.T) {
	drainTimeout = 50 * time.Millisecond
	defer func() { drainTimeout = time.Minute }()
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			t.Cleanup(func() { conn.Close() })
		}
	}()
	done := make(chan error, 1)
	go func() { done <- Forward(port, strings.NewReader(""), io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
}

func TestAServerStillTalkingAfterTheEndOfInputIsNotCutOff(t *testing.T) {
	drainTimeout = 200 * time.Millisecond
	defer func() { drainTimeout = time.Minute }()
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for range 8 {
			time.Sleep(50 * time.Millisecond)
			conn.Write([]byte("x"))
		}
	}()
	var out bytes.Buffer
	if err := Forward(port, strings.NewReader(""), &out); err != nil || out.Len() != 8 {
		t.Errorf("got %d bytes, %v", out.Len(), err)
	}
}

func TestAServerThatNeverStopsTalkingIsLeftAtTheLimit(t *testing.T) {
	drainLimit = 100 * time.Millisecond
	defer func() { drainLimit = 10 * time.Minute }()
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, err := conn.Write([]byte("tick")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- Forward(port, strings.NewReader(""), io.Discard) }() // no write fails: the engine reads a gone client's stdout
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
}
