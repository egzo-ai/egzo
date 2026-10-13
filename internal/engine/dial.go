// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/client"
)

// Target says where an engine is, without reading the environment or any file of the host: it is how a
// process that holds several engines (the hub) opens each of them.
type Target struct {
	// Host is unix:///path or tcp://host:port. The other schemes need DialContext.
	Host string
	// CA, Cert and Key are PEM. With a CA (or a Cert) a tcp host is reached over TLS; Cert and Key make it mutual.
	CA, Cert, Key []byte
	// DialContext, when set, makes every connection. It is how a transport egzo does not know (ssh)
	// plugs in: Host then only names the engine in messages.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial opens the engine a Target describes and probes it, like Connect.
func Dial(ctx context.Context, target Target) (*Client, error) {
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return nil, errors.New("no engine host")
	}
	options := []client.Opt{client.WithHost(host), client.WithAPIVersionNegotiation()}
	if target.DialContext != nil {
		options = append(options, client.WithDialContext(target.DialContext))
	}
	if strings.HasPrefix(host, "tcp://") && (len(target.CA) > 0 || len(target.Cert) > 0) {
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if len(target.CA) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(target.CA) {
				return nil, errors.New("the CA is not valid PEM")
			}
			config.RootCAs = pool
		}
		if len(target.Cert) > 0 || len(target.Key) > 0 {
			pair, err := tls.X509KeyPair(target.Cert, target.Key)
			if err != nil {
				return nil, fmt.Errorf("client certificate: %w", err)
			}
			config.Certificates = []tls.Certificate{pair}
		}
		options = append(options, client.WithScheme("https"), client.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: config, Proxy: nil},
		}))
	} else if strings.HasPrefix(host, "tcp://") && target.DialContext == nil {
		options = append(options, client.WithScheme("http"))
	}
	api, err := client.NewClientWithOpts(options...)
	if err != nil {
		return nil, err
	}
	return probe(ctx, api, "")
}

// probe pings the engine and learns what it is. hint is added to the error when nothing answers.
func probe(ctx context.Context, api *client.Client, hint string) (*Client, error) {
	host := api.DaemonHost()
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := api.Ping(check); err != nil {
		api.Close()
		return nil, fmt.Errorf("no container engine reachable at %s: %w%s", host, err, hint)
	}
	c := &Client{API: api, Host: host}
	if info, err := api.Info(check); err == nil {
		for _, option := range info.SecurityOptions {
			c.Rootless = c.Rootless || strings.Contains(option, "rootless")
		}
	}
	if version, err := api.ServerVersion(check); err == nil {
		c.Podman = strings.Contains(strings.ToLower(version.Platform.Name), "podman")
		for _, component := range version.Components {
			c.Podman = c.Podman || strings.Contains(strings.ToLower(component.Name), "podman")
		}
	}
	return c, nil
}
