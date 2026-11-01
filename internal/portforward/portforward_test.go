// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package portforward

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
	if err := Forward(port, strings.NewReader(""), io.Discard, Options{}); !errors.As(err, &closed) || closed.Port != port {
		t.Errorf("got %v", err)
	}
}

func TestALoopbackThatCannotBeAskedCountsAsNothingListening(t *testing.T) {
	for _, reason := range []syscall.Errno{syscall.EAFNOSUPPORT, syscall.EPROTONOSUPPORT, syscall.EADDRNOTAVAIL, syscall.ENETUNREACH, syscall.ECONNREFUSED} {
		if err := (&net.OpError{Op: "dial", Err: os.NewSyscallError("socket", reason)}); !unreachable(err) {
			t.Errorf("%v is a failure to report", reason)
		}
	}
	if unreachable(&net.OpError{Op: "dial", Err: os.NewSyscallError("socket", syscall.EMFILE)}) {
		t.Error("a machine out of descriptors is not a closed port")
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
	if err := Forward(port, blocked(t), &out, Options{}); err != nil || out.String() != "six" {
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
	if err := Forward(port, strings.NewReader("all of it"), &out, Options{}); err != nil || out.String() != "read all of it" {
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
	go func() { done <- Forward(port, strings.NewReader(""), io.Discard, Options{}) }()
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
	if err := Forward(port, strings.NewReader(""), &out, Options{}); err != nil || out.Len() != 8 {
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
	go func() { done <- Forward(port, strings.NewReader(""), io.Discard, Options{}) }() // no write fails: the engine reads a gone client's stdout
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
}

func TestConnectedIsCalledBeforeAnyByteAndNotWhenNothingListens(t *testing.T) {
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Write([]byte("hello"))
			conn.Close()
		}
	}()
	var out bytes.Buffer
	seen := -1
	err := Forward(port, blocked(t), &out, Options{Connected: func() { seen = out.Len() }})
	if err != nil || seen != 0 || out.String() != "hello" {
		t.Errorf("err %v, seen after %d bytes, got %q", err, seen, out.String())
	}
	listener.Close()
	called := false
	if err := Forward(port, strings.NewReader(""), io.Discard, Options{Connected: func() { called = true }}); err == nil || called {
		t.Errorf("err %v, called %v", err, called)
	}
}

func TestCloseOnEOFDoesNotWaitForAServerThatKeepsQuiet(t *testing.T) {
	listener, port := listen(t, "tcp4", "127.0.0.1:0")
	go func() {
		if conn, err := listener.Accept(); err == nil {
			defer conn.Close()
			time.Sleep(time.Hour) // the server says nothing and closes nothing
		}
	}()
	done := make(chan error, 1)
	go func() { done <- Forward(port, strings.NewReader("bye"), io.Discard, Options{CloseOnEOF: true}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("it waited for the server")
	}
}

func TestAnnouncedCommand(t *testing.T) {
	if got := strings.Join(AnnouncedCommand(3000, false), " "); got != "egzo agent forward --announce 3000" {
		t.Error(got)
	}
	if got := strings.Join(AnnouncedCommand(3000, true), " "); got != "egzo agent forward --announce --close-on-eof 3000" {
		t.Error(got)
	}
}
