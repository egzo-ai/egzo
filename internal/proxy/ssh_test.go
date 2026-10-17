// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---- a fake ssh server standing in for the machine an agent administers ----

type fakeSSHD struct {
	t        *testing.T
	listener net.Listener
	hostKey  ssh.Signer
	allowed  ssh.PublicKey // the one key it accepts: the proxy's
	mu       sync.Mutex
	users    []string
	requests []string // "type payload"
	closed   chan struct{}
	extra    []ssh.Signer // more host keys, of other types
}

func newKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

func startFakeSSHD(t *testing.T, allowed ssh.PublicKey) *fakeSSHD {
	t.Helper()
	hostKey, _ := newKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSHD{t: t, listener: listener, hostKey: hostKey, allowed: allowed, closed: make(chan struct{}, 16)}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSSHD) record(kind string, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, kind+" "+string(payload))
}

func (f *fakeSSHD) saw(kind string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.HasPrefix(r, kind+" ") {
			return true
		}
	}
	return false
}

func (f *fakeSSHD) serve(conn net.Conn) {
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		f.mu.Lock()
		f.users = append(f.users, meta.User())
		f.mu.Unlock()
		if !bytes.Equal(key.Marshal(), f.allowed.Marshal()) {
			return nil, errors.New("unknown key")
		}
		return nil, nil
	}}
	config.AddHostKey(f.hostKey)
	f.mu.Lock()
	extra := f.extra
	f.mu.Unlock()
	for _, key := range extra {
		config.AddHostKey(key)
	}
	sc, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sc.Close()
	go func() {
		for r := range requests {
			if r.WantReply {
				r.Reply(r.Type == keepAliveRequest, nil)
			}
		}
	}()
	for ch := range channels {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "")
			continue
		}
		channel, reqs, _ := ch.Accept()
		go f.session(channel, reqs)
	}
}

// session runs what its tests ask for: "echo", "err", "exit N", "cat", "wait" and a shell that greets.
func (f *fakeSSHD) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { f.closed <- struct{}{} }()
	for r := range requests {
		f.record(r.Type, r.Payload)
		switch r.Type {
		case "exec":
			var command execRequest
			ssh.Unmarshal(r.Payload, &command)
			if r.WantReply {
				r.Reply(true, nil)
			}
			var status uint32
			switch {
			case command.Command == "echo":
				io.WriteString(channel, "hello\n")
			case command.Command == "err":
				io.WriteString(channel.Stderr(), "oops\n")
			case strings.HasPrefix(command.Command, "exit "):
				status = uint32(command.Command[5] - '0')
			case command.Command == "cat":
				io.Copy(channel, channel)
			case command.Command == "wait":
				io.Copy(io.Discard, channel)
				continue
			case command.Command == "big":
				chunk := bytes.Repeat([]byte("x"), 1<<20)
				for i := 0; i < 4; i++ {
					channel.Write(chunk)
				}
			}
			if command.Command != "wait" {
				channel.CloseWrite()
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				channel.Close()
			}
		case "shell":
			r.Reply(true, nil)
			io.WriteString(channel, "welcome\n")
			channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			channel.Close()
		default:
			if r.WantReply {
				r.Reply(true, nil)
			}
		}
	}
}

// ---- the proxy under test ----

type sshRig struct {
	t        *testing.T
	server   *Server
	audit    *safeBuffer
	address  string // the proxy's ssh listener
	hostKey  ssh.Signer
	identity ssh.Signer // the agent's key
	fake     *fakeSSHD
	proxyKey ssh.Signer // the key the profile holds for the remote server
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		var e Event
		if json.Unmarshal([]byte(line), &e) == nil {
			events = append(events, e)
		}
	}
	return events
}

func (s *safeBuffer) has(action, containing string) bool {
	for _, e := range s.events() {
		if e.Action == action && strings.Contains(e.Reason+e.Command, containing) {
			return true
		}
	}
	return false
}

func newSSHRig(t *testing.T, mutate func(*SSHServer), tune ...func(*Server)) *sshRig {
	t.Helper()
	proxyKey, proxyKeyPEM := newKey(t)
	fake := startFakeSSHD(t, proxyKey.PublicKey())
	identity, _ := newKey(t)
	hostKey, _ := newKey(t)

	audit := &safeBuffer{}
	server := NewServer(nil, NewAudit(audit))
	server.SetSSHHostKey(hostKey)
	server.DialSSH = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fake.listener.Addr().String())
	}
	server.SSHKeepAlive = time.Hour
	for _, f := range tune {
		f(server)
	}

	remote := SSHServer{
		Name: "prod", Host: "prod.internal", Port: 22, User: "deploy", Key: proxyKeyPEM,
		HostKey: ssh.FingerprintSHA256(fake.hostKey.PublicKey()),
	}
	if mutate != nil {
		mutate(&remote)
	}
	server.SetPolicy(&Policy{Hash: "h", Profiles: map[string]Profile{"ops": {SSH: []SSHServer{remote}}, "empty": {}}})
	if err := server.BindAgent("a1", Binding{Token: "t", Profile: "ops", SSHKey: string(ssh.MarshalAuthorizedKey(identity.PublicKey()))}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go server.ServeSSH(listener)
	return &sshRig{t: t, server: server, audit: audit, address: listener.Addr().String(), hostKey: hostKey, identity: identity, fake: fake, proxyKey: proxyKey}
}

func (r *sshRig) dial(user string, signer ssh.Signer) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(r.hostKey.PublicKey()),
		Timeout:         5 * time.Second,
	}
	return ssh.Dial("tcp", r.address, config)
}

func (r *sshRig) connect() *ssh.Client {
	r.t.Helper()
	client, err := r.dial("prod", r.identity)
	if err != nil {
		r.t.Fatalf("sign in: %v", err)
	}
	r.t.Cleanup(func() { client.Close() })
	return client
}

func run(t *testing.T, client *ssh.Client, command string) (stdout, stderr string, err error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var out, errOut bytes.Buffer
	session.Stdout, session.Stderr = &out, &errOut
	err = session.Run(command)
	return out.String(), errOut.String(), err
}

// ---- specs of the listener ----

func TestSSHRunsACommandOnTheServerAndAuditsIt(t *testing.T) {
	r := newSSHRig(t, nil)
	out, _, err := run(t, r.connect(), "echo")
	if err != nil || out != "hello\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	if !r.audit.has("ssh-exec", "echo") || !r.audit.has("ssh-open", "") {
		t.Fatalf("audit: %+v", r.audit.events())
	}
	// The server saw the user of the profile, not the one the agent typed.
	if r.fake.users[0] != "deploy" {
		t.Fatalf("users %v", r.fake.users)
	}
}

func TestSSHRelaysStderrAndExitStatus(t *testing.T) {
	r := newSSHRig(t, nil)
	client := r.connect()
	_, stderr, err := run(t, client, "err")
	if err != nil || stderr != "oops\n" {
		t.Fatalf("stderr %q err %v", stderr, err)
	}
	_, _, err = run(t, client, "exit 3")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("err %v", err)
	}
}

func TestSSHRelaysInputAndEndOfInput(t *testing.T) {
	r := newSSHRig(t, nil)
	session, _ := r.connect().NewSession()
	session.Stdin = strings.NewReader("some input")
	out, err := session.Output("cat")
	if err != nil || string(out) != "some input" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestSSHRelaysALargeOutput(t *testing.T) {
	r := newSSHRig(t, nil)
	out, _, err := run(t, r.connect(), "big")
	if err != nil || len(out) != 4<<20 {
		t.Fatalf("got %d bytes, err %v", len(out), err)
	}
}

func TestSSHRelaysAShellWithATerminal(t *testing.T) {
	r := newSSHRig(t, nil)
	session, _ := r.connect().NewSession()
	if err := session.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	session.Stdout = &out
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "welcome") || !r.fake.saw("pty-req") {
		t.Fatalf("out %q requests %v", out.String(), r.fake.requests)
	}
	if !r.audit.has("ssh-shell", "") {
		t.Fatal("the shell is audited")
	}
}

func TestSSHClosingTheAgentsSessionEndsTheRemoteCommand(t *testing.T) {
	r := newSSHRig(t, nil)
	session, _ := r.connect().NewSession()
	if err := session.Start("wait"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	session.Close()
	select {
	case <-r.fake.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote session outlived the agent's")
	}
}

func TestSSHRefusesAnIdentityNobodyHas(t *testing.T) {
	r := newSSHRig(t, nil)
	stranger, _ := newKey(t)
	if _, err := r.dial("prod", stranger); err == nil {
		t.Fatal("an unknown key signed in")
	}
	if len(r.fake.users) != 0 {
		t.Fatal("the server was contacted")
	}
}

func TestSSHRefusesAServerThatIsNotInTheProfile(t *testing.T) {
	r := newSSHRig(t, nil)
	for _, user := range []string{"staging", "deploy", "root", ""} {
		if _, err := r.dial(user, r.identity); err == nil {
			t.Fatalf("signed in for %q", user)
		}
	}
}

func TestSSHRefusesAnAgentWhoseProfileHasNoServer(t *testing.T) {
	r := newSSHRig(t, nil)
	other, _ := newKey(t)
	if err := r.server.BindAgent("a2", Binding{Token: "t2", Profile: "empty", SSHKey: string(ssh.MarshalAuthorizedKey(other.PublicKey()))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dial("prod", other); err == nil {
		t.Fatal("an agent of another profile reached the server")
	}
}

func TestSSHAnAgentWithoutAnIdentityHasNoAccess(t *testing.T) {
	r := newSSHRig(t, nil)
	if err := r.server.BindAgent("a3", Binding{Token: "t3", Profile: "ops"}); err != nil {
		t.Fatal(err)
	}
	empty, _ := newKey(t)
	if _, err := r.dial("prod", empty); err == nil {
		t.Fatal("signed in")
	}
}

func TestSSHOffersPublicKeyAuthenticationOnly(t *testing.T) {
	r := newSSHRig(t, nil)
	for name, method := range map[string]ssh.AuthMethod{
		"password": ssh.Password("x"),
		"keyboard": ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil }),
	} {
		config := &ssh.ClientConfig{User: "prod", Auth: []ssh.AuthMethod{method}, HostKeyCallback: ssh.FixedHostKey(r.hostKey.PublicKey()), Timeout: 5 * time.Second}
		client, err := ssh.Dial("tcp", r.address, config)
		if err == nil {
			client.Close()
			t.Fatalf("%s authentication was accepted", name)
		}
		if !strings.Contains(err.Error(), "publickey") && !strings.Contains(err.Error(), "unable to authenticate") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSSHRefusesForwarding(t *testing.T) {
	r := newSSHRig(t, nil)
	client := r.connect()
	if conn, err := client.Dial("tcp", "10.0.0.1:80"); err == nil {
		conn.Close()
		t.Fatal("direct-tcpip was relayed")
	}
	if listener, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		listener.Close()
		t.Fatal("tcpip-forward was accepted")
	}
	session, _ := client.NewSession()
	defer session.Close()
	if ok, _ := session.SendRequest("auth-agent-req@openssh.com", true, nil); ok {
		t.Fatal("agent forwarding was accepted")
	}
	if ok, _ := session.SendRequest("x11-req", true, nil); ok {
		t.Fatal("X11 forwarding was accepted")
	}
	if err := session.RequestSubsystem("netconf"); err == nil {
		t.Fatal("a subsystem other than sftp was relayed")
	}
	if r.fake.saw("auth-agent-req@openssh.com") || r.fake.saw("subsystem") {
		t.Fatalf("a refused request reached the server: %v", r.fake.requests)
	}
}

func TestSSHRelaysTheSftpSubsystemAndAuditsIt(t *testing.T) {
	r := newSSHRig(t, nil)
	session, _ := r.connect().NewSession()
	defer session.Close()
	if err := session.RequestSubsystem("sftp"); err != nil {
		t.Fatalf("sftp: %v", err)
	}
	if !r.fake.saw("subsystem") || !r.audit.has("ssh-sftp", "") {
		t.Fatalf("requests %v audit %+v", r.fake.requests, r.audit.events())
	}
}

func TestSSHRefusesAServerWhoseHostKeyIsNotThePinnedOne(t *testing.T) {
	other, _ := newKey(t)
	r := newSSHRig(t, func(s *SSHServer) { s.HostKey = ssh.FingerprintSHA256(other.PublicKey()) })
	_, stderr, err := run(t, r.connect(), "echo")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 255 || !strings.Contains(stderr, "host key") {
		t.Fatalf("stderr %q err %v", stderr, err)
	}
	if len(r.fake.users) != 0 {
		t.Fatal("the key was offered to a server that is not the pinned one")
	}
	if !r.audit.has("ssh-error", ssh.FingerprintSHA256(r.fake.hostKey.PublicKey())) {
		t.Fatalf("the audit should name the key the server presented: %+v", r.audit.events())
	}
}

func TestSSHPinsAPublicKeyLineToo(t *testing.T) {
	r := newSSHRig(t, nil)
	policy := *r.server.policy.Load()
	profile := policy.Profiles["ops"]
	profile.SSH[0].HostKey = string(ssh.MarshalAuthorizedKey(r.fake.hostKey.PublicKey()))
	r.server.SetPolicy(&policy)
	if out, _, err := run(t, r.connect(), "echo"); err != nil || out != "hello\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	profile.SSH[0].HostKey = "ssh-ed25519 " + strings.SplitN(string(ssh.MarshalAuthorizedKey(r.hostKey.PublicKey())), " ", 2)[1]
	r.server.SetPolicy(&policy)
	if _, _, err := run(t, r.connect(), "echo"); err == nil {
		t.Fatal("the wrong public key was accepted")
	}
}

func TestSSHAFingerprintPinFindsTheKeyTypeOfAServerWithSeveral(t *testing.T) {
	r := newSSHRig(t, nil)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(ecdsaKey)
	r.fake.mu.Lock()
	r.fake.extra = []ssh.Signer{signer}
	r.fake.mu.Unlock()
	policy := *r.server.policy.Load()
	policy.Profiles["ops"].SSH[0].HostKey = ssh.FingerprintSHA256(signer.PublicKey())
	r.server.SetPolicy(&policy)
	if out, _, err := run(t, r.connect(), "echo"); err != nil || out != "hello\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestSSHTheServerRefusingTheKeyIsReportedWithoutTheKey(t *testing.T) {
	r := newSSHRig(t, nil)
	r.fake.allowed = r.identity.PublicKey() // not the proxy's key
	_, stderr, err := run(t, r.connect(), "echo")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || !strings.Contains(stderr, "refused the key") {
		t.Fatalf("stderr %q err %v", stderr, err)
	}
	if strings.Contains(stderr, "PRIVATE") {
		t.Fatal("key material in the message")
	}
}

func TestSSHAnUnreadableKeyNeverQuotesIt(t *testing.T) {
	r := newSSHRig(t, func(s *SSHServer) { s.Key = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-material\n" })
	_, stderr, _ := run(t, r.connect(), "echo")
	if !strings.Contains(stderr, "not usable") || strings.Contains(stderr, "secret-material") {
		t.Fatalf("stderr %q", stderr)
	}
	for _, e := range r.audit.events() {
		if strings.Contains(e.Reason, "secret-material") {
			t.Fatal("the audit quotes the key")
		}
	}
}

func TestSSHUnbindingTheAgentClosesItsConnections(t *testing.T) {
	r := newSSHRig(t, nil)
	client := r.connect()
	session, _ := client.NewSession()
	if err := session.Start("wait"); err != nil {
		t.Fatal(err)
	}
	r.server.UnbindAgent("a1")
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection of an unbound agent stayed open")
	}
	if _, err := r.dial("prod", r.identity); err == nil {
		t.Fatal("signed in again")
	}
}

func TestSSHChangingTheIdentityClosesTheOldConnections(t *testing.T) {
	r := newSSHRig(t, nil)
	client := r.connect()
	session, _ := client.NewSession()
	session.Start("wait")
	next, _ := newKey(t)
	if err := r.server.BindAgent("a1", Binding{Token: "t", Profile: "ops", SSHKey: string(ssh.MarshalAuthorizedKey(next.PublicKey()))}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the old identity's connection stayed open")
	}
	if _, err := r.dial("prod", r.identity); err == nil {
		t.Fatal("the old identity still signs in")
	}
	if c, err := r.dial("prod", next); err != nil {
		t.Fatalf("the new one does not: %v", err)
	} else {
		c.Close()
	}
}

func TestSSHAnIdleConnectionIsClosed(t *testing.T) {
	r := newSSHRig(t, nil, func(s *Server) { s.SSHIdleTimeout = 200 * time.Millisecond })
	client := r.connect()
	session, _ := client.NewSession()
	session.Start("wait")
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a silent connection stayed open")
	}
	if !r.audit.has("ssh-error", "idle") {
		t.Fatalf("audit: %+v", r.audit.events())
	}
}

func TestSSHLimitsWhatOneAgentHolds(t *testing.T) {
	r := newSSHRig(t, nil, func(s *Server) { s.MaxPerAgent = 2 })
	a, b := r.connect(), r.connect()
	_ = a
	_ = b
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := r.dial("prod", r.identity)
		if err != nil {
			return
		}
		// The connection was accepted at the handshake and closed right after.
		_, _, rerr := run2(c)
		c.Close()
		if rerr != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a third connection was held for one agent")
}

func run2(client *ssh.Client) (string, string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()
	return "", "", session.Run("echo")
}

func TestSSHTooManySessionsAreRefused(t *testing.T) {
	r := newSSHRig(t, nil)
	client := r.connect()
	var held []*ssh.Session
	for i := 0; i < maxSSHSessions; i++ {
		s, err := client.NewSession()
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		if err := s.Start("wait"); err != nil {
			t.Fatal(err)
		}
		held = append(held, s)
	}
	if s, err := client.NewSession(); err == nil {
		s.Close()
		t.Fatal("an eleventh session was opened")
	}
	for _, s := range held {
		s.Close()
	}
}

func TestSSHDialRefusesLoopbackAndMetadataButNotPrivateAddresses(t *testing.T) {
	for address, ok := range map[string]bool{
		"127.0.0.1": false, "::1": false, "0.0.0.0": false, "224.0.0.1": false,
		"169.254.169.254": false, "169.254.170.2": false, "fd00:ec2::254": false, "100.100.100.200": false,
		"10.0.0.5": true, "192.168.1.9": true, "172.16.3.4": true, "169.254.1.2": true, "93.184.216.34": true,
	} {
		err := sshDialAllowed(net.ParseIP(address))
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v, want %v (%v)", address, err == nil, ok, err)
		}
	}
}

func TestSSHTheIdentityKeyIsEd25519Only(t *testing.T) {
	r := newSSHRig(t, nil)
	for _, line := range []string{"", "not a key", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7", string(ssh.MarshalAuthorizedKey(r.identity.PublicKey())) + string(ssh.MarshalAuthorizedKey(r.identity.PublicKey()))} {
		if line == "" {
			continue
		}
		if err := r.server.BindAgent("x", Binding{Token: "t", Profile: "ops", SSHKey: line}); err == nil {
			t.Errorf("%q was accepted", line)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSSHHostKeyIsKept(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateSSHHostKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateSSHHostKey(dir)
	if err != nil || !bytes.Equal(first.PublicKey().Marshal(), again.PublicKey().Marshal()) {
		t.Fatalf("the host key changed: %v", err)
	}
	if info, _ := os.Stat(dir + "/" + sshHostKeyFile); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestSSHClientFilesNameEveryServerAndTrustOnlyTheProxy(t *testing.T) {
	dir := t.TempDir()
	hostKey, _ := newKey(t)
	policy := &Policy{Profiles: map[string]Profile{
		"a": {SSH: []SSHServer{{Name: "prod"}, {Name: "db"}}},
		"b": {SSH: []SSHServer{{Name: "prod"}}},
	}}
	if err := PublishSSHClientFiles(dir, hostKey.PublicKey(), policy); err != nil {
		t.Fatal(err)
	}
	config := readFile(t, dir+"/ssh_config")
	if strings.Count(config, "\nHost prod\n") != 1 || !strings.Contains(config, "\nHost db\n") {
		t.Fatalf("config:\n%s", config)
	}
	for _, line := range []string{"HostName proxy", "Port 2222", "User prod", "StrictHostKeyChecking yes", "ForwardAgent no", "IdentitiesOnly yes"} {
		if !strings.Contains(config, "    "+line+"\n") {
			t.Errorf("missing %q", line)
		}
	}
	known := readFile(t, dir+"/ssh_known_hosts")
	if !strings.HasPrefix(known, "egzo-proxy ssh-ed25519 ") || !strings.Contains(known, strings.Fields(string(ssh.MarshalAuthorizedKey(hostKey.PublicKey())))[1]) {
		t.Fatalf("known_hosts %q", known)
	}
	// With no policy there is nothing to name, and the host key is still published.
	if err := PublishSSHClientFiles(dir, hostKey.PublicKey(), nil); err != nil || strings.Contains(readFile(t, dir+"/ssh_config"), "Host ") {
		t.Fatalf("empty: %v", err)
	}
}

func TestSSHIdentityIsDerivedFromTheTokenAlone(t *testing.T) {
	line, err := IdentityPublicKey("token-1")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := IdentityPublicKey("token-1")
	other, _ := IdentityPublicKey("token-2")
	if line != again || line == other || !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("%q %q %q", line, again, other)
	}
	if _, err := IdentityPublicKey(""); err == nil {
		t.Fatal("an empty token has an identity")
	}
	private, _ := IdentityPrivateKey("token-1")
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) != line {
		t.Fatalf("private half: %v", err)
	}
	if err := checkIdentityKey(line); err != nil {
		t.Fatalf("the proxy refuses what it derives: %v", err)
	}
}
