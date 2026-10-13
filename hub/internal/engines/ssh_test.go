package engines

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/egzo-ai/egzo/internal/engine"
)

func newKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(private)
	return signer, pem.EncodeToMemory(block)
}

// sshServer accepts one public key and forwards direct-streamlocal channels to a local unix socket.
type sshServer struct {
	addr        string
	fingerprint string
}

func startSSHServer(t *testing.T, authorized ssh.PublicKey, socketPath string) sshServer {
	t.Helper()
	hostSigner, _ := newKey(t)
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) == string(authorized.Marshal()) {
			return nil, nil
		}
		return nil, io.EOF
	}}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go func() {
					for r := range requests {
						r.Reply(r.Type == "keepalive@openssh.com", nil)
					}
				}()
				for ch := range channels {
					if ch.ChannelType() != "direct-streamlocal@openssh.com" {
						ch.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					var payload struct {
						Path      string
						Reserved1 string
						Reserved2 uint32
					}
					if err := ssh.Unmarshal(ch.ExtraData(), &payload); err != nil || payload.Path != "/var/run/docker.sock" {
						ch.Reject(ssh.ConnectionFailed, "bad path")
						continue
					}
					local, err := net.Dial("unix", socketPath)
					if err != nil {
						ch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					channel, reqs, _ := ch.Accept()
					go ssh.DiscardRequests(reqs)
					go func() { io.Copy(channel, local); channel.CloseWrite() }()
					go func() { io.Copy(local, channel); local.Close() }()
				}
				server.Close()
			}()
		}
	}()
	return sshServer{addr: listener.Addr().String(), fingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey())}
}

func fakeSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.43")
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/{v}/info", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	mux.HandleFunc("/{v}/version", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return path
}

func TestAnEngineBehindSSHIsReachedWithTheHostKeyPinned(t *testing.T) {
	clientSigner, clientPEM := newKey(t)
	server := startSSHServer(t, clientSigner.PublicKey(), fakeSocket(t))
	host := "ssh://tester@" + server.addr

	// first contact: nothing pinned, the operator is shown the fingerprint
	probe, err := NewSSHTransport(host, "", clientPEM, "")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := probe.Test(context.Background())
	probe.Close()
	if err != nil || fingerprint != server.fingerprint {
		t.Fatalf("test = %q, %v; want %q", fingerprint, err, server.fingerprint)
	}

	// pinned: the engine answers through the tunnel
	transport, _ := NewSSHTransport(host, "", clientPEM, server.fingerprint)
	defer transport.Close()
	c, err := engine.Dial(context.Background(), engine.Target{Host: "tcp://engine.over.ssh", DialContext: transport.DialContext})
	if err != nil {
		t.Fatalf("dial through ssh: %v", err)
	}
	c.Close()
}

func TestAChangedHostKeyRefusesToConnect(t *testing.T) {
	clientSigner, clientPEM := newKey(t)
	server := startSSHServer(t, clientSigner.PublicKey(), fakeSocket(t))
	transport, _ := NewSSHTransport("ssh://tester@"+server.addr, "", clientPEM, "SHA256:somethingelse")
	defer transport.Close()
	_, err := transport.Test(context.Background())
	var changed *HostKeyError
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("err = %v, want a host key error", err)
	}
	if !asHostKeyError(err, &changed) || changed.Seen != server.fingerprint {
		t.Errorf("the error does not carry the fingerprint it saw: %v", err)
	}
}

func asHostKeyError(err error, target **HostKeyError) bool {
	for err != nil {
		if e, ok := err.(*HostKeyError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestAnUnknownKeyIsRefusedAndAnAddressMustBeSSH(t *testing.T) {
	authorized, _ := newKey(t)
	_, strangerPEM := newKey(t)
	server := startSSHServer(t, authorized.PublicKey(), fakeSocket(t))
	transport, _ := NewSSHTransport("ssh://tester@"+server.addr, "", strangerPEM, "")
	if _, err := transport.Test(context.Background()); err == nil {
		t.Error("a key the server does not know was accepted")
	}
	for _, host := range []string{"tcp://x:1", "ssh://", "not a url"} {
		if _, err := NewSSHTransport(host, "me", strangerPEM, ""); err == nil {
			t.Errorf("%q accepted", host)
		}
	}
	if _, err := NewSSHTransport("ssh://host", "", strangerPEM, ""); err == nil {
		t.Error("an ssh engine without a user was accepted")
	}
	if _, err := NewSSHTransport("ssh://me@host", "", []byte("junk"), ""); err == nil {
		t.Error("junk accepted as a private key")
	}
}
