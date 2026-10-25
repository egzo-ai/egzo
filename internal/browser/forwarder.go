// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Chromium sends proxy credentials only in answer to a 407, and only its pages answer one: Playwright supplies the credentials
// per page, so Chromium's own services (push messaging, update checks) stop at the challenge. The forwarder is the proxy
// Chromium is given instead: it listens on loopback, adds the agent's credentials to each CONNECT and passes the bytes through,
// so everything Chromium sends reaches the egzo proxy as the agent. See roadmap/proxy-client-identity.md.

// upstream is the egzo proxy of HTTPS_PROXY: where to connect, and the Proxy-Authorization to send, empty without credentials.
type upstream struct {
	address       string
	authorization string
}

// upstreamFrom reads HTTPS_PROXY. Errors never carry the URL, which holds the credentials.
func upstreamFrom(raw string) (upstream, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return upstream{}, errors.New("HTTPS_PROXY is not a URL")
	}
	if parsed.Scheme != "http" {
		return upstream{}, errors.New("HTTPS_PROXY is not an http:// URL")
	}
	if parsed.Hostname() == "" {
		return upstream{}, errors.New("HTTPS_PROXY has no host")
	}
	port := parsed.Port()
	if port == "" {
		port = "3128"
	}
	up := upstream{address: net.JoinHostPort(parsed.Hostname(), port)}
	if parsed.User != nil {
		// url.Userinfo hands back the decoded name and password: a token or a password with `@`, `:` or `%` works.
		password, _ := parsed.User.Password()
		up.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(parsed.User.Username()+":"+password))
	}
	return up, nil
}

const (
	// headTimeout bounds how long a client takes to send its CONNECT.
	headTimeout = 30 * time.Second
	// dialTimeout bounds how long the egzo proxy takes to answer the connection.
	dialTimeout = 10 * time.Second
)

// forwarder is the loopback proxy Chromium is given.
type forwarder struct {
	listener net.Listener
	up       upstream
}

// listenForwarder opens the forwarder on a free port of 127.0.0.1: nothing outside the container reaches it.
func listenForwarder(up upstream) (*forwarder, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open the browser's proxy forwarder: %w", err)
	}
	return &forwarder{listener: listener, up: up}, nil
}

// URL is the proxy URL Chromium is given: no credentials.
func (f *forwarder) URL() string { return "http://" + f.listener.Addr().String() }

func (f *forwarder) Close() error { return f.listener.Close() }

// serve relays connections until the forwarder is closed.
func (f *forwarder) serve() {
	for {
		client, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.relay(client)
	}
}

// relay takes one connection: a CONNECT is sent on to the egzo proxy with the credentials and the client's User-Agent, and then
// the bytes go both ways untouched (the proxy's answer, the TLS). Anything else is refused as the egzo proxy refuses it: a web page
// can reach a loopback port, but it can never send a CONNECT.
func (f *forwarder) relay(client net.Conn) {
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(headTimeout))
	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if request.Method != http.MethodConnect {
		refuse(client, http.StatusMethodNotAllowed, "egzo proxy supports CONNECT only")
		return
	}
	proxy, err := net.DialTimeout("tcp", f.up.address, dialTimeout)
	if err != nil {
		refuse(client, http.StatusBadGateway, "egzo: the proxy did not answer")
		return
	}
	defer proxy.Close()
	head := "CONNECT " + request.Host + " HTTP/1.1\r\nHost: " + request.Host + "\r\n"
	if f.up.authorization != "" {
		head += "Proxy-Authorization: " + f.up.authorization + "\r\n"
	}
	if agent := request.Header.Get("User-Agent"); agent != "" {
		head += "User-Agent: " + agent + "\r\n"
	}
	if _, err := io.WriteString(proxy, head+"\r\n"); err != nil {
		return
	}
	client.SetReadDeadline(time.Time{})

	// What the client sent behind its CONNECT is still in the reader: it goes first.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		io.Copy(proxy, reader)
		if tcp, ok := proxy.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	io.Copy(client, proxy)
	client.Close()
	proxy.Close()
	<-sent
}

func refuse(client net.Conn, status int, text string) {
	fmt.Fprintf(client, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(text), text)
}
