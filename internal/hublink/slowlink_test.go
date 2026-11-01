// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// slowConn takes as long to write as a slow uplink does, and one writer at a time: what sits in the buffers of a real path is left out, so
// this is the least a person would feel.
type slowConn struct {
	net.Conn
	bytesPerSecond int
}

func (c slowConn) Write(data []byte) (int, error) {
	time.Sleep(time.Duration(len(data)) * time.Second / time.Duration(c.bytesPerSecond))
	return c.Conn.Write(data)
}

func TestLargePreviewsLeaveATerminalUsableOnASlowLink(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a few seconds on purpose")
	}
	const rate = 1 << 18 // 2 Mbit/s up
	near, far := net.Pipe()
	link, err := yamux.Client(slowConn{near, rate}, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	hub, err := yamux.Server(slowConn{far, rate}, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	// three downloads from an agent, through the link, in the pieces an exec gives
	stop := make(chan struct{})
	defer close(stop)
	for range 3 {
		stream, err := link.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := hub.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		go io.Copy(io.Discard, accepted)
		go func() {
			chunk := make([]byte, 32<<10)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := stream.Write(chunk); err != nil {
					return
				}
			}
		}()
	}

	// a terminal: a key from the hub, its echo from the link
	terminal, err := hub.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	echo, err := link.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(echo, echo)
	var worst time.Duration
	key := make([]byte, 1)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		began := time.Now()
		terminal.SetDeadline(began.Add(9 * time.Second))
		if _, err := terminal.Write(key); err != nil {
			t.Fatalf("after %v: %v", time.Since(began), err)
		}
		if _, err := io.ReadFull(terminal, key); err != nil {
			t.Fatalf("after %v: %v", time.Since(began), err)
		}
		worst = max(worst, time.Since(began))
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("the slowest key took %v at 2 Mbit/s with three downloads", worst)
	if worst > 2*time.Second {
		t.Errorf("a key took %v", worst)
	}
	if link.IsClosed() || hub.IsClosed() {
		t.Error("the connection ended")
	}
}

func TestAPortConnectionOutlivesItsDialAndHalfCloses(t *testing.T) {
	peer, link, _ := proved(t)
	got := make(chan Request, 1)
	answering(t, link, func(request Request, lines *Lines, stream *yamux.Stream) {
		got <- request
		// the first bytes share a write with the reply: they sit in the reader that read it
		data, _ := json.Marshal(Reply{Done: true})
		stream.Write(append(append(data, '\n'), "banner:"...))
		received, _ := io.ReadAll(lines.Rest()) // until the half close
		stream.Write(append([]byte("got "), received...))
		stream.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := peer.DialPort(ctx, "coder-1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	if request := <-got; request.Verb != VerbForward || request.Agent != "coder-1" || request.Port != 3000 {
		t.Errorf("%+v", request)
	}
	if _, err := io.WriteString(conn, "hello"); err != nil {
		t.Fatalf("the end of the dial's context reached the connection: %v", err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	answer, err := io.ReadAll(conn)
	if err != nil || string(answer) != "banner:got hello" {
		t.Errorf("%q %v", answer, err)
	}
}

func TestAPortThatCannotBeReachedIsAnAnswer(t *testing.T) {
	peer, link, _ := proved(t)
	answering(t, link, func(_ Request, lines *Lines, _ *yamux.Stream) {
		lines.Write(Reply{Done: true, Error: "nothing listens on port 3000"})
	})
	if _, err := peer.DialPort(context.Background(), "coder-1", 3000); err == nil || err.Error() != "nothing listens on port 3000" {
		t.Error(err)
	}
}

func TestADialIsCutByItsContext(t *testing.T) {
	peer, link, _ := proved(t)
	answering(t, link, func(Request, *Lines, *yamux.Stream) { select {} })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := peer.DialPort(ctx, "coder-1", 3000); !errors.Is(err, context.DeadlineExceeded) {
		t.Error(err)
	}
}
