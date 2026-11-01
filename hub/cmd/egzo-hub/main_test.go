package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, handler http.Handler, read time.Duration) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(listener.Addr().String(), handler, read)
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}

func TestABodyThatIsStillComingIsCutOff(t *testing.T) {
	done := make(chan error, 1)
	addr := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		done <- err
	}), 100*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "POST / HTTP/1.1\r\nHost: hub\r\nContent-Length: 100\r\n\r\nstart")
	select {
	case err := <-done:
		if err == nil {
			t.Error("a body that never ended was read to its end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was held open")
	}
}

func TestAHijackedConnectionOutlivesTheLimits(t *testing.T) {
	read := 100 * time.Millisecond
	addr := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		time.Sleep(3 * read)
		line, err := buffered.ReadString('\n')
		if err != nil {
			t.Errorf("the connection was cut: %v", err)
			return
		}
		io.WriteString(conn, strings.ToUpper(line))
	}), read)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: hub\r\n\r\n")
	time.Sleep(3 * read)
	io.WriteString(conn, "hello\n")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || line != "HELLO\n" {
		t.Errorf("answer = %q, %v", line, err)
	}
}
