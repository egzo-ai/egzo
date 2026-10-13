package engines

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// HostKeyError means the engine's ssh host key is not the one pinned for it.
type HostKeyError struct{ Host, Pinned, Seen string }

func (e *HostKeyError) Error() string {
	return fmt.Sprintf("the ssh host key of %s changed: pinned %s, the server now presents %s; refusing to connect "+
		"(if the key was replaced on purpose, remove the engine and add it again)", e.Host, e.Pinned, e.Seen)
}

// SSHTransport reaches an engine's unix socket through an ssh connection (direct-streamlocal), the way `docker -H ssh://`
// does, with the host key pinned. It reconnects when the connection is lost.
type SSHTransport struct {
	addr, user string
	socket     string
	config     *ssh.ClientConfig

	connectMu sync.Mutex
	mu        sync.Mutex
	client    *ssh.Client
	seen      string
}

// NewSSHTransport reads ssh://user@host[:port][/path/to/docker.sock]. pinned is the host key fingerprint to require, empty to
// accept whatever key is presented (the caller then reads Fingerprint and asks the operator).
func NewSSHTransport(host, defaultUser string, privateKey []byte, pinned string) (*SSHTransport, error) {
	u, err := url.Parse(host)
	if err != nil || u.Scheme != "ssh" || u.Hostname() == "" {
		return nil, fmt.Errorf("%q is not an ssh://user@host[:port][/path/to/docker.sock] address", host)
	}
	user := u.User.Username()
	if user == "" {
		user = defaultUser
	}
	if user == "" {
		return nil, errors.New("an ssh engine needs a user")
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("ssh private key: %w", err)
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	socket := u.Path
	if socket == "" || socket == "/" {
		socket = "/var/run/docker.sock"
	}
	t := &SSHTransport{addr: net.JoinHostPort(u.Hostname(), port), user: user, socket: socket}
	t.config = &ssh.ClientConfig{
		User:    user,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(signer)},
		Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint := ssh.FingerprintSHA256(key)
			t.mu.Lock()
			t.seen = fingerprint
			t.mu.Unlock()
			if pinned != "" && fingerprint != pinned {
				return &HostKeyError{Host: t.addr, Pinned: pinned, Seen: fingerprint}
			}
			return nil
		},
	}
	return t, nil
}

// Fingerprint is the host key the server last presented (SHA256:...), once connected.
func (t *SSHTransport) Fingerprint() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen
}

func (t *SSHTransport) connect(ctx context.Context) (*ssh.Client, error) {
	t.connectMu.Lock() // one handshake at a time
	defer t.connectMu.Unlock()
	t.mu.Lock()
	client := t.client
	t.mu.Unlock()
	if client != nil {
		// a keepalive tells a live connection from one the network dropped
		if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
			return client, nil
		}
		client.Close()
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return nil, err
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, t.addr, t.config)
	if err != nil {
		conn.Close()
		var hostKey *HostKeyError
		if errors.As(err, &hostKey) {
			return nil, hostKey
		}
		return nil, fmt.Errorf("ssh %s@%s: %w", t.user, t.addr, err)
	}
	client = ssh.NewClient(clientConn, channels, requests)
	t.mu.Lock()
	t.client = client
	t.mu.Unlock()
	return client, nil
}

// DialContext opens a connection to the engine's socket on the server; the network and address it is asked for are ignored.
func (t *SSHTransport) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, err := t.connect(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial("unix", t.socket)
	if err != nil {
		return nil, fmt.Errorf("open %s on %s: %w", t.socket, t.addr, err)
	}
	return conn, nil
}

// Test connects and returns the fingerprint of the host key, closing again.
func (t *SSHTransport) Test(ctx context.Context) (string, error) {
	if _, err := t.connect(ctx); err != nil {
		return t.Fingerprint(), err
	}
	return t.Fingerprint(), nil
}

func (t *SSHTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client == nil {
		return nil
	}
	err := t.client.Close()
	t.client = nil
	return err
}

// IsSSH reports whether host is an ssh:// address.
func IsSSH(host string) bool { return strings.HasPrefix(host, "ssh://") }
