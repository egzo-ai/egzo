// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// The ssh listener is the ssh analogue of a TLS-intercepted host. An agent signs in with the key egzo made for its
// instance (the name of the server is the user it asks for); the proxy then opens its own connection to that server with the
// key from the profile and relays the sessions between the two. The key never reaches the agent, only the sessions of
// servers its profile names can be opened, and nothing is relayed that is not a plain session: no port forwarding, no
// agent forwarding, no X11.

const (
	defaultSSHKeepAlive    = 30 * time.Second
	defaultSSHIdleTimeout  = time.Hour
	maxSSHSessions         = 10
	maxSSHHandshakes       = 64
	keepAliveRequest       = "keepalive@openssh.com"
	keepAliveFailuresLimit = 3
)

// SetSSHHostKey sets the key the ssh listener identifies itself with.
func (s *Server) SetSSHHostKey(key ssh.Signer) { s.sshHostKey.Store(&key) }

// ServeSSH accepts ssh connections from agents on listener until it is closed.
func (s *Server) ServeSSH(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveSSHConn(conn)
	}
}

func (s *Server) sshKeepAlive() time.Duration {
	if s.SSHKeepAlive <= 0 {
		return defaultSSHKeepAlive
	}
	return s.SSHKeepAlive
}

func (s *Server) sshIdleTimeout() time.Duration {
	if s.SSHIdleTimeout <= 0 {
		return defaultSSHIdleTimeout
	}
	return s.SSHIdleTimeout
}

// sshIdentity finds the agent whose identity key is key. It looks at every binding, so the time it takes does not tell
// how many agents there are or which one matched.
func (s *Server) sshIdentity(key ssh.PublicKey) (agent string, binding Binding, ok bool) {
	current := s.agents.Load()
	if current == nil {
		return "", Binding{}, false
	}
	wire := key.Marshal()
	for name, candidate := range *current {
		if candidate.SSHKey == "" {
			continue
		}
		known, _, _, _, err := ssh.ParseAuthorizedKey([]byte(candidate.SSHKey))
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(known.Marshal(), wire) == 1 {
			agent, binding, ok = name, candidate, true
		}
	}
	return agent, binding, ok
}

// sshServerOf finds the server an agent's profile names.
func (s *Server) sshServerOf(agent, name string) (SSHServer, bool) {
	policy := s.policy.Load()
	current := s.agents.Load()
	if policy == nil || current == nil {
		return SSHServer{}, false
	}
	binding, ok := (*current)[agent]
	if !ok {
		return SSHServer{}, false
	}
	for _, server := range policy.Profiles[binding.Profile].SSH {
		if server.Name == name {
			return server, true
		}
	}
	return SSHServer{}, false
}

func (s *Server) sshServerConfig() (*ssh.ServerConfig, error) {
	hostKey := s.sshHostKey.Load()
	if hostKey == nil {
		return nil, errors.New("the ssh listener has no host key")
	}
	config := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-egzo",
		MaxAuthTries:  3,
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			// What is decided here is returned in the permissions of this very key: x/crypto calls the callback for every key
			// a client offers, and only the one that signs is the identity (CVE-2024-45337).
			agent, binding, ok := s.sshIdentity(key)
			if !ok {
				return nil, errors.New("unknown identity")
			}
			if _, ok := s.sshServerOf(agent, meta.User()); !ok {
				return nil, errors.New("no such server")
			}
			return &ssh.Permissions{Extensions: map[string]string{
				"agent": agent, "instance": binding.Instance, "profile": binding.Profile, "server": meta.User(),
			}}, nil
		},
	}
	config.AddHostKey(*hostKey)
	return config, nil
}

func (s *Server) serveSSHConn(raw net.Conn) {
	defer raw.Close()
	if s.handshakes.Add(1) > maxSSHHandshakes {
		s.handshakes.Add(-1)
		return
	}
	c := newConn(s.audit, SSH)
	config, err := s.sshServerConfig()
	if err != nil {
		s.handshakes.Add(-1)
		c.decide(KindConnect, OutcomeError, because(CodeConfig, err.Error()))
		return
	}
	raw.SetDeadline(time.Now().Add(s.handshakeTimeout()))
	sc, channels, requests, err := ssh.NewServerConn(raw, config)
	s.handshakes.Add(-1)
	if err != nil {
		// Nothing about the attempt is trusted, so it is recorded without a name.
		c.decide(KindConnect, OutcomeDeny, because(CodeIdentity, "no valid identity for a server of a profile"))
		return
	}
	raw.SetDeadline(time.Time{})
	extensions := sc.Permissions.Extensions
	agent, name := extensions["agent"], extensions["server"]
	server, ok := s.sshServerOf(agent, name)
	if !ok {
		sc.Close()
		return
	}
	c.from(agent, extensions["instance"], extensions["profile"])
	c.host, c.server = server.Host, name
	release, reason := s.limits.acquire(agent, s.MaxPerAgent, s.MaxTotal)
	if release == nil {
		c.decide(KindConnect, OutcomeDeny, because(CodeLimit, reason))
		sc.Close()
		return
	}
	defer release()

	conn := &sshConn{s: s, agent: agent, audit: c, name: name, client: sc, started: time.Now()}
	conn.server.Store(&server)
	conn.touch()
	if !s.trackSSH(agent, conn) {
		sc.Close()
		return
	}
	defer s.untrackSSH(agent, conn)
	c.decide(KindConnect, OutcomeAllow, nil)
	conn.run(channels, requests)
}

// A bound agent's ssh connections are tracked so that unbinding it, or changing its identity, closes them.
func (s *Server) trackSSH(agent string, conn *sshConn) bool {
	s.sshMu.Lock()
	defer s.sshMu.Unlock()
	if s.sshConns == nil {
		s.sshConns = map[string]map[*sshConn]struct{}{}
	}
	current := s.agents.Load()
	if current == nil {
		return false
	}
	if _, ok := (*current)[agent]; !ok {
		return false
	}
	if s.sshConns[agent] == nil {
		s.sshConns[agent] = map[*sshConn]struct{}{}
	}
	s.sshConns[agent][conn] = struct{}{}
	return true
}

func (s *Server) untrackSSH(agent string, conn *sshConn) {
	s.sshMu.Lock()
	defer s.sshMu.Unlock()
	delete(s.sshConns[agent], conn)
	if len(s.sshConns[agent]) == 0 {
		delete(s.sshConns, agent)
	}
}

// closeSSH closes every ssh connection of an agent.
func (s *Server) closeSSH(agent string) {
	s.sshMu.Lock()
	var open []*sshConn
	for conn := range s.sshConns[agent] {
		open = append(open, conn)
	}
	s.sshMu.Unlock()
	for _, conn := range open {
		conn.client.Close()
	}
}

// sshConn is one agent's connection to the listener, and the one connection to the server that its sessions share.
type sshConn struct {
	s       *Server
	agent   string
	audit   *conn                     // the host follows the entry in use
	server  atomic.Pointer[SSHServer] // the entry in use: the one at sign-in, then the one dialed
	name    string
	client  *ssh.ServerConn
	started time.Time

	last atomic.Int64 // when data last moved, Unix nanoseconds
	up   atomic.Int64 // bytes from the agent to the server
	down atomic.Int64

	idled atomic.Bool // closed by the proxy for being idle

	mu       sync.Mutex
	upstream *ssh.Client
	failed   *dialFailure
	dialed   bool
}

func (c *sshConn) touch() { c.last.Store(time.Now().UnixNano()) }

// log writes an event of the connection, with the host of the entry in use.
func (c *sshConn) log(event Event) {
	event.Host = c.server.Load().Host
	c.audit.log(event)
}

func (c *sshConn) decide(k Kind, decision Outcome, reason *Reason) {
	c.log(Event{Kind: k, Decision: decision, Reason: reason})
}

// connect dials the server once for all the sessions of this connection.
func (c *sshConn) connect() (*ssh.Client, *dialFailure) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialed {
		return c.upstream, c.failed
	}
	c.dialed = true
	// A server whose entry changed or went away since the agent signed in is not dialed with the old entry.
	server, ok := c.s.sshServerOf(c.agent, c.name)
	if !ok {
		c.failed = &dialFailure{Code: CodeNotInProfile, Public: "this server is not in your egress profile any more", Detail: "server no longer in the profile"}
		return nil, c.failed
	}
	c.server.Store(&server)
	client, failed := c.s.dialSSH(context.Background(), server)
	if failed != nil {
		c.failed = failed
		c.decide(KindUpstream, OutcomeError, because(failed.Code, failed.Detail))
		return nil, failed
	}
	c.upstream = client
	go keepAlive(client, c.s.sshKeepAlive(), func() { client.Close() })
	return client, nil
}

func (c *sshConn) run(channels <-chan ssh.NewChannel, requests <-chan *ssh.Request) {
	done := make(chan struct{})
	defer close(done)
	go keepAlive(c.client, c.s.sshKeepAlive(), func() { c.client.Close() })
	go c.watchIdle(done)

	go func() {
		for request := range requests {
			ok := false
			switch request.Type {
			case keepAliveRequest, "no-more-sessions@openssh.com":
				ok = true
			default:
				// tcpip-forward, streamlocal-forward and the rest: nothing is listened on for an agent.
				c.decide(KindRequest, OutcomeDeny, because(CodeForwarding, "global request refused: "+request.Type))
			}
			if request.WantReply {
				request.Reply(ok, nil)
			}
		}
	}()

	sessions := make(chan struct{}, maxSSHSessions)
	var wg sync.WaitGroup
	for channel := range channels {
		if channel.ChannelType() != "session" {
			// direct-tcpip, forwarded-tcpip, x11, streamlocal: the agent gets a shell or a command, never a way onward.
			c.decide(KindChannel, OutcomeDeny, because(CodeChannel, "channel refused: "+channel.ChannelType()))
			channel.Reject(ssh.Prohibited, "egzo only relays ssh sessions")
			continue
		}
		select {
		case sessions <- struct{}{}:
		default:
			channel.Reject(ssh.ResourceShortage, "too many sessions")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sessions }()
			c.session(channel)
		}()
	}
	wg.Wait()
	c.mu.Lock()
	if c.upstream != nil {
		c.upstream.Close()
	}
	c.mu.Unlock()
	var reason *Reason
	if c.idled.Load() {
		reason = because(CodeIdle, "closed after being idle")
	}
	c.log(Event{Kind: KindClose, BytesUp: c.up.Load(), BytesDown: c.down.Load(), DurationMs: time.Since(c.started).Milliseconds(), Reason: reason})
}

// watchIdle closes the connection when no session has moved data for the idle timeout.
func (c *sshConn) watchIdle(done <-chan struct{}) {
	idle := c.s.sshIdleTimeout()
	tick := idle / 4
	if tick <= 0 {
		tick = time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, c.last.Load())) > idle {
				c.idled.Store(true)
				c.client.Close()
				return
			}
		}
	}
}

// keepAlive probes a connection and closes it when the other side stops answering.
func keepAlive(conn interface {
	SendRequest(string, bool, []byte) (bool, []byte, error)
}, every time.Duration, closeConn func()) {
	failures := 0
	for {
		time.Sleep(every)
		answered := make(chan error, 1)
		go func() {
			_, _, err := conn.SendRequest(keepAliveRequest, true, nil)
			answered <- err
		}()
		select {
		case err := <-answered:
			if err != nil {
				// The connection is closed: this ends the loop.
				return
			}
			failures = 0
		case <-time.After(every):
			if failures++; failures >= keepAliveFailuresLimit {
				closeConn()
				return
			}
		}
	}
}

type execRequest struct{ Command string }

// session relays one session channel: its requests both ways and its data.
func (c *sshConn) session(newChannel ssh.NewChannel) {
	upstream, failed := c.connect()
	if failed != nil {
		c.refuse(newChannel, failed.Public)
		return
	}
	remote, remoteRequests, err := upstream.OpenChannel("session", nil)
	if err != nil {
		c.decide(KindUpstream, OutcomeError, because(CodeSession, "could not open a session on the server: "+err.Error()))
		c.refuse(newChannel, "the server did not open a session")
		return
	}
	local, localRequests, err := newChannel.Accept()
	if err != nil {
		remote.Close()
		return
	}

	// What the server sends ends when it closes the channel; what the agent sends ends when the agent's channel closes,
	// which happens after.
	var fromServer, fromAgent sync.WaitGroup
	copyData := func(wg *sync.WaitGroup, dst io.Writer, src io.Reader, counter *atomic.Int64) {
		defer wg.Done()
		buffer := make([]byte, 32<<10)
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				c.touch()
				counter.Add(int64(n))
				if _, werr := dst.Write(buffer[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	fromServer.Add(3)
	go copyData(&fromServer, local, remote, &c.down)
	go copyData(&fromServer, local.Stderr(), remote.Stderr(), &c.down)
	go func() { // the server's requests (exit status and signal) to the agent
		defer fromServer.Done()
		for request := range remoteRequests {
			ok, _ := local.SendRequest(request.Type, request.WantReply, request.Payload)
			if request.WantReply {
				request.Reply(ok, nil)
			}
		}
	}()

	var forwarding sync.Mutex // held while a request of the agent is being passed on and answered
	fromAgent.Add(2)
	go func() { // the agent's input, then end of input
		copyData(&fromAgent, remote, local, &c.up)
		remote.CloseWrite()
	}()
	go func() { // the agent's requests to the server
		defer fromAgent.Done()
		for request := range localRequests {
			forwarding.Lock()
			c.forward(request, remote)
			forwarding.Unlock()
		}
		// The agent's channel is closed: so is the command it ran.
		remote.Close()
	}()
	fromServer.Wait()
	// The server may close the channel right after answering a request: the agent has that answer before the channel closes.
	func() {
		forwarding.Lock()
		defer forwarding.Unlock()
	}()
	local.Close()
	remote.Close()
	fromAgent.Wait()
}

// forward passes one request of the agent to the server, or refuses it.
func (c *sshConn) forward(request *ssh.Request, remote ssh.Channel) {
	switch request.Type {
	case "pty-req", "window-change", "env", "signal", "break", "shell", "exec":
		switch request.Type {
		case "exec":
			var command execRequest
			if ssh.Unmarshal(request.Payload, &command) != nil {
				c.reply(request, false)
				return
			}
			c.log(Event{Kind: KindExec, Decision: OutcomeAllow, Command: command.Command})
		case "shell":
			c.decide(KindShell, OutcomeAllow, nil)
		}
		ok, err := remote.SendRequest(request.Type, request.WantReply, request.Payload)
		c.reply(request, ok && err == nil)
	case "subsystem":
		var subsystem struct{ Name string }
		if ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
			c.decide(KindRequest, OutcomeDeny, because(CodeRequest, "request refused: subsystem"))
			c.reply(request, false)
			return
		}
		c.decide(KindSFTP, OutcomeAllow, nil)
		ok, err := remote.SendRequest(request.Type, request.WantReply, request.Payload)
		c.reply(request, ok && err == nil)
	default:
		// x11-req, auth-agent-req@openssh.com and anything unknown.
		c.decide(KindRequest, OutcomeDeny, because(CodeRequest, "request refused: "+request.Type))
		c.reply(request, false)
	}
}

func (c *sshConn) reply(request *ssh.Request, ok bool) {
	if request.WantReply {
		request.Reply(ok, nil)
	}
}

// refuse answers a session that cannot be relayed the way ssh itself would: the reason on standard error and exit status 255.
func (c *sshConn) refuse(newChannel ssh.NewChannel, reason string) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	go func() {
		for request := range requests {
			c.reply(request, request.Type == "shell" || request.Type == "exec" || request.Type == "pty-req" || request.Type == "env")
			if request.Type == "shell" || request.Type == "exec" {
				fmt.Fprintf(channel.Stderr(), "egzo: %s\r\n", strings.TrimSpace(reason))
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{255}))
				channel.Close()
				return
			}
		}
	}()
}
