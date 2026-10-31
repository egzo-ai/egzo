// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hublink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Dialer says how a link reaches its hub.
type Dialer struct {
	Hub   string // https://hub.example.org
	CA    []byte // PEM: the CA of the hub's certificate, when it is not one of the system's
	Proxy string // a company proxy, credentials included; "" goes direct
}

// ErrUpgradeRefused says something between the link and the hub dropped the WebSocket upgrade. Knowing which is the whole help.
var ErrUpgradeRefused = errors.New("the WebSocket upgrade was refused on the way to the hub (HTTP 426): a reverse proxy without the " +
	"Upgrade and Connection headers, or a company proxy that inspects TLS and does not pass WebSockets")

// errorsOnly passes on what yamux says when it ends a session; its warnings are about streams that closed in passing.
type errorsOnly struct{}

func (errorsOnly) Print(v ...any) {
	if line := fmt.Sprint(v...); strings.HasPrefix(line, "[ERR]") {
		fmt.Fprintln(os.Stderr, line)
	}
}

func (errorsOnly) Printf(format string, v ...any) { errorsOnly{}.Print(fmt.Sprintf(format, v...)) }

func (errorsOnly) Println(v ...any) { errorsOnly{}.Print(fmt.Sprint(v...)) }

func yamuxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	config.LogOutput = nil
	config.Logger = errorsOnly{}
	config.KeepAliveInterval = KeepAlive
	config.ConnectionWriteTimeout = 10 * time.Second
	config.MaxStreamWindowSize = 1 << 20
	return config
}

// Dial connects to the hub and returns the session, as the side that opens the control stream.
func (d Dialer) Dial(ctx context.Context) (*yamux.Session, error) {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}}
	if len(d.CA) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(d.CA) {
			return nil, errors.New("the hub's CA is not valid PEM")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	if d.Proxy != "" {
		proxy, err := url.Parse(d.Proxy)
		if err != nil {
			return nil, errors.New("the proxy's address is not valid") // the error would show its password
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	conn, resp, err := websocket.Dial(ctx, strings.TrimRight(d.Hub, "/")+Path, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport}, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
			return nil, ErrUpgradeRefused
		}
		return nil, fmt.Errorf("reach the hub: %w", err)
	}
	return yamux.Client(netConn(conn), yamuxConfig())
}

// Accept takes a link's WebSocket and returns the hub's end of the session.
func Accept(w http.ResponseWriter, r *http.Request) (*yamux.Session, error) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	return yamux.Server(netConn(conn), yamuxConfig())
}

// netConn is the WebSocket as a byte stream. The context is its whole life; yamux frames its own data, so there is no read limit.
func netConn(conn *websocket.Conn) net.Conn {
	conn.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
}
