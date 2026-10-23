// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// A host key that is not the pinned one. Seen is what the server presented.
type hostKeyMismatch struct{ Seen string }

func (e *hostKeyMismatch) Error() string {
	return "the server's host key is not the pinned one (it presented " + e.Seen + ")"
}

// A pinned key is a SHA256 fingerprint, or a public key line. The fingerprint does not say which key type the server
// has, so a server with several is tried type by type (see dialSSH).
type pin struct {
	fingerprint string
	key         ssh.PublicKey
}

func parsePin(value string) (pin, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "SHA256:") {
		return pin{fingerprint: value}, nil
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(value))
	if err != nil {
		return pin{}, errors.New("the pinned host key is not a fingerprint or a public key")
	}
	return pin{key: key}, nil
}

func (p pin) check(key ssh.PublicKey) error {
	seen := ssh.FingerprintSHA256(key)
	var equal bool
	if p.key != nil {
		equal = bytes.Equal(p.key.Marshal(), key.Marshal())
	} else {
		equal = subtle.ConstantTimeCompare([]byte(p.fingerprint), []byte(seen)) == 1
	}
	if !equal {
		return &hostKeyMismatch{Seen: seen}
	}
	return nil
}

// algorithms are the host key algorithms to ask for. A public key pins its own type; a fingerprint pins whatever type the
// server has, so every type is tried in turn.
func (p pin) algorithms() [][]string {
	if p.key != nil {
		switch p.key.Type() {
		case ssh.KeyAlgoRSA:
			return [][]string{{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}}
		default:
			return [][]string{{p.key.Type()}}
		}
	}
	return [][]string{
		nil, // the client's own order first: nearly every server answers with its best key
		{ssh.KeyAlgoED25519},
		{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521},
		{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
	}
}

// metadata are the addresses of cloud metadata services. A server of a profile is one an operator named, so a private
// address is fine (the typical target is inside), but these hand out credentials to whoever asks.
var metadata = []net.IP{
	net.ParseIP("169.254.169.254"), net.ParseIP("169.254.170.2"), net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"), net.ParseIP("168.63.129.16"),
}

func sshDialAllowed(ip net.IP) error {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%s is not an address a server can be reached at", ip)
	}
	for _, blocked := range metadata {
		if blocked.Equal(ip) {
			return fmt.Errorf("%s is a metadata service address", ip)
		}
	}
	return nil
}

// sshControl is a net.Dialer Control function, run on the address a name resolved to just before connecting.
func sshControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%s is not an address", host)
	}
	return sshDialAllowed(ip)
}

// dialFailure says, in a sentence safe to show an agent, why a server could not be reached.
type dialFailure struct {
	Code   Code   // why, for the audit
	Public string // what the agent is told
	Detail string // what the audit records
}

func (f *dialFailure) Error() string { return f.Detail }

func failure(code Code, public string, err error) *dialFailure {
	return &dialFailure{Code: code, Public: public, Detail: public + ": " + err.Error()}
}

// dialSSH connects to a server of a profile as its user with its key, checks the pinned host key, and returns the client.
func (s *Server) dialSSH(ctx context.Context, server SSHServer) (*ssh.Client, *dialFailure) {
	pinned, err := parsePin(server.HostKey)
	if err != nil {
		return nil, failure(CodeHostKey, "the server's host key is not usable", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(server.Key))
	if err != nil {
		// The error of the parser can quote the key; say nothing of it.
		return nil, &dialFailure{Code: CodeKey, Public: "the key for this server is not usable", Detail: "the private key could not be parsed"}
	}
	address := net.JoinHostPort(server.Host, strconv.Itoa(server.Port))

	var mismatch *hostKeyMismatch
	var last error
	for _, algorithms := range pinned.algorithms() {
		conn, err := s.dialSSHConn(ctx, address)
		if err != nil {
			return nil, failure(CodeDial, "could not connect to the server", err)
		}
		config := &ssh.ClientConfig{
			User:              server.User,
			Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback:   func(_ string, _ net.Addr, key ssh.PublicKey) error { return pinned.check(key) },
			HostKeyAlgorithms: algorithms,
			ClientVersion:     "SSH-2.0-egzo",
		}
		conn.SetDeadline(time.Now().Add(s.handshakeTimeout()))
		client, channels, requests, err := ssh.NewClientConn(conn, address, config)
		if err == nil {
			conn.SetDeadline(time.Time{})
			return ssh.NewClient(client, channels, requests), nil
		}
		conn.Close()
		last = err
		var seen *hostKeyMismatch
		if errors.As(err, &seen) {
			if mismatch == nil {
				mismatch = seen
			}
			continue
		}
		if strings.Contains(err.Error(), "no common algorithm for host key") {
			continue
		}
		break
	}
	switch {
	case mismatch != nil:
		return nil, &dialFailure{Code: CodeHostKey, Public: "the server's host key is not the pinned one", Detail: mismatch.Error()}
	case last != nil && strings.Contains(last.Error(), "unable to authenticate"):
		return nil, failure(CodeAuth, "the server refused the key", last)
	}
	return nil, failure(CodeDial, "could not open an ssh connection to the server", last)
}

func (s *Server) dialSSHConn(ctx context.Context, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	dial := s.DialSSH
	if dial == nil {
		dial = (&net.Dialer{Timeout: dialTimeout, Control: sshControl}).DialContext
	}
	return dial(ctx, "tcp", address)
}
