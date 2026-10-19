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
	"sync"
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
	return probe(ctx, api, "", false)
}

// probe pings the engine and learns what it is. hint is added to the error when nothing answers.
func probe(ctx context.Context, api *client.Client, hint string, remembered bool) (*Client, error) {
	host := api.DaemonHost()
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := api.Ping(check); err != nil {
		api.Close()
		return nil, fmt.Errorf("no container engine reachable at %s: %w%s", host, err, hint)
	}
	key := ""
	if remembered {
		key = rememberKey(host)
	}
	return &Client{API: api, Host: host, facts: &facts{key: key}}, nil
}

// facts is what egzo learns about an engine when it first needs it. The two questions cost an engine a call each, and rootless
// Podman answers each in about 350 ms (it asks its runtime and conmon for their versions), so a command that needs neither does not ask.
type facts struct {
	info, version sync.Once
	// key says which engine the answers are kept for (see remembered.go); empty when they are not kept.
	key string
}

// IsPodman reports whether the engine behind the Docker-compatible API is Podman.
func (c *Client) IsPodman() bool {
	if c.facts != nil {
		c.facts.version.Do(func() {
			if value, found := recall(c.facts.key, "podman"); found {
				c.Podman = value
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if version, err := c.API.ServerVersion(ctx); err == nil {
				c.Podman = strings.Contains(strings.ToLower(version.Platform.Name), "podman")
				for _, component := range version.Components {
					c.Podman = c.Podman || strings.Contains(strings.ToLower(component.Name), "podman")
				}
				remember(c.facts.key, "podman", c.Podman)
			}
		})
	}
	return c.Podman
}

// IsRootless reports whether the engine runs without root: it maps users itself, like Podman.
func (c *Client) IsRootless() bool {
	if c.facts != nil {
		c.facts.info.Do(func() {
			if value, found := recall(c.facts.key, "rootless"); found {
				c.Rootless = value
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if info, err := c.API.Info(ctx); err == nil {
				for _, option := range info.SecurityOptions {
					c.Rootless = c.Rootless || strings.Contains(option, "rootless")
				}
				remember(c.facts.key, "rootless", c.Rootless)
			}
		})
	}
	return c.Rootless
}
