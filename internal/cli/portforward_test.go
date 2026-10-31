// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/portforward"
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

// failingListener answers Accept with these errors, then ends.
type failingListener struct {
	net.Listener
	errors []error
}

func (l *failingListener) Accept() (net.Conn, error) {
	if len(l.errors) == 0 {
		return nil, net.ErrClosed
	}
	err := l.errors[0]
	l.errors = l.errors[1:]
	return nil, err
}

func TestRunningOutOfDescriptorsPausesAccepting(t *testing.T) {
	var said []string
	f := forwarder{name: "web", port: 3000, report: func(line string) { said = append(said, line) }}
	emfile := &net.OpError{Op: "accept", Err: syscall.EMFILE}
	listener := &failingListener{errors: []error{emfile, emfile, &net.OpError{Op: "accept", Err: syscall.ENFILE}}}
	if _, err := f.accept(context.Background(), listener); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("got %v", err)
	}
	if len(listener.errors) != 0 {
		t.Errorf("gave up with %d errors to go", len(listener.errors))
	}
	if len(said) != 1 || !strings.Contains(said[0], "web:3000") {
		t.Errorf("said %q", said)
	}
}

func TestAnotherErrorEndsAccepting(t *testing.T) {
	f := forwarder{report: func(string) { t.Error("nothing to say") }}
	if _, err := f.accept(context.Background(), &failingListener{errors: []error{syscall.EINVAL}}); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("got %v", err)
	}
}

func TestACancelledContextEndsThePause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := forwarder{report: func(string) {}}
	emfile := make([]error, 1000)
	for i := range emfile {
		emfile[i] = syscall.EMFILE
	}
	if _, err := f.accept(ctx, &failingListener{errors: emfile}); !errors.Is(err, syscall.EMFILE) {
		t.Errorf("got %v", err)
	}
}

func TestTheImageIsBlamedOnlyWhenItIsAtFault(t *testing.T) {
	notEgzo := "does not contain egzo"
	cases := []struct {
		name   string
		result engine.ExecResult
		err    error
		want   string
	}{
		{name: "it refuses the port", result: engine.ExecResult{Stderr: []byte(`"0" ` + portforward.NotAPort), ExitCode: 1}},
		{name: "not in the image", result: engine.ExecResult{ExitCode: 127}, want: notEgzo},
		{name: "cannot run it", result: engine.ExecResult{ExitCode: 126}, want: notEgzo},
		{name: "older", result: engine.ExecResult{Stderr: []byte("unknown command"), ExitCode: 1}, want: notEgzo},
		{name: "start failed", err: errors.New(`exec: "egzo": executable file not found in $PATH`), want: notEgzo},
		{name: "engine failure", err: errors.New("server error 500"), want: "server error 500"},
		{name: "killed", result: engine.ExecResult{ExitCode: 137}, want: "killed"},
	}
	for _, c := range cases {
		err := probed("web", c.result, c.err)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := probed("web", engine.ExecResult{}, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("the cause is lost: %v", err)
	}
}

func TestAFailureSaysSomethingEvenWithoutWords(t *testing.T) {
	if got := lastWords("  nothing listens on port 3000\n").reason(1); got != "nothing listens on port 3000" {
		t.Errorf("got %q", got)
	}
	if got := lastWords(nil).reason(137); got != "the forward ended with code 137" {
		t.Errorf("got %q", got)
	}
}

func TestAWriteToAClientThatLeftIsNoted(t *testing.T) {
	client, accepted := pair(t)
	client.Close()
	accepted.Close()
	w := clientWriter{Writer: accepted}
	if _, err := w.Write([]byte("x")); err == nil || !w.left {
		t.Errorf("got %v, left %v", err, w.left)
	}
	fine := clientWriter{Writer: io.Discard}
	if _, err := fine.Write([]byte("x")); err != nil || fine.left {
		t.Errorf("got %v, left %v", err, fine.left)
	}
}
