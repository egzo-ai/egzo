// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// fakeProxy accepts one connection, hands the CONNECT it read to the test, answers 200 and then echoes what follows.
func fakeProxy(t *testing.T) (address string, heads chan *http.Request) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	heads = make(chan *http.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		heads <- request
		io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		io.Copy(conn, reader)
	}()
	return listener.Addr().String(), heads
}

func forwarding(t *testing.T, up upstream) *forwarder {
	t.Helper()
	f, err := listenForwarder(up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	go f.serve()
	if !strings.HasPrefix(f.URL(), "http://127.0.0.1:") {
		t.Fatalf("the forwarder listens on %s", f.URL())
	}
	return f
}

func dialForwarder(t *testing.T, f *forwarder) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.URL(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestAConnectIsSentOnWithTheCredentialsAndTheUserAgentAndThenTheBytesGoBothWays(t *testing.T) {
	address, heads := fakeProxy(t)
	f := forwarding(t, upstream{address: address, authorization: "Basic Y29kZXI6dG9r"})
	conn := dialForwarder(t, f)
	// what a client sends right behind its CONNECT is not lost
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nUser-Agent: Chrome/155\r\n\r\nhello")
	head := <-heads
	if head.Method != http.MethodConnect || head.Host != "example.com:443" {
		t.Errorf("CONNECT %s", head.Host)
	}
	if head.Header.Get("Proxy-Authorization") != "Basic Y29kZXI6dG9r" || head.Header.Get("User-Agent") != "Chrome/155" {
		t.Errorf("headers = %v", head.Header)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response %v, %v", response, err)
	}
	echoed := make([]byte, 5)
	if _, err := io.ReadFull(reader, echoed); err != nil || string(echoed) != "hello" {
		t.Errorf("echoed %q, %v", echoed, err)
	}
}

func TestTheClientsOwnCredentialsAreNotPassedOn(t *testing.T) {
	address, heads := fakeProxy(t)
	f := forwarding(t, upstream{address: address, authorization: "Basic Y29kZXI6dG9r"})
	conn := dialForwarder(t, f)
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic b3RoZXI6eA==\r\n\r\n")
	if got := (<-heads).Header.Values("Proxy-Authorization"); len(got) != 1 || got[0] != "Basic Y29kZXI6dG9r" {
		t.Errorf("Proxy-Authorization = %v", got)
	}
}

func TestAnythingButAConnectIsRefusedAndNeverReachesTheProxy(t *testing.T) {
	address, heads := fakeProxy(t)
	f := forwarding(t, upstream{address: address, authorization: "Basic Y29kZXI6dG9r"})
	conn := dialForwarder(t, f)
	// what a web page can send to a loopback port: never a CONNECT
	io.WriteString(conn, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("response %v, %v", response, err)
	}
	select {
	case head := <-heads:
		t.Errorf("the proxy was asked for %s", head.Host)
	default:
	}
}

func TestAProxyThatDoesNotAnswerIsABadGateway(t *testing.T) {
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	address := listener.Addr().String()
	listener.Close()
	f := forwarding(t, upstream{address: address})
	conn := dialForwarder(t, f)
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("response %v, %v", response, err)
	}
}
