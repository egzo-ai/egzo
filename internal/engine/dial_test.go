// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine answers the three calls Dial makes, as a rootless Podman would.
func answeringEngine() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.43")
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/{v}/info", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"SecurityOptions":["name=rootless"]}`))
	})
	mux.HandleFunc("/{v}/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Platform":{"Name":"Podman Engine"}}`))
	})
	return mux
}

func TestDialAUnixSocketWithoutTheEnvironment(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///does/not/exist")
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: answeringEngine()}
	go server.Serve(listener)
	defer server.Close()

	c, err := Dial(context.Background(), Target{Host: "unix://" + socket})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.IsPodman() || !c.IsRootless() {
		t.Errorf("Podman=%v Rootless=%v: the engine's answers were not read", c.IsPodman(), c.IsRootless())
	}
	if !strings.HasSuffix(c.Host, "engine.sock") {
		t.Errorf("host = %q", c.Host)
	}
}

// A command that needs neither fact does not pay for them: Podman takes about 350 ms to answer each (rootless).
func TestDialAsksWhatTheEngineIsOnlyWhenSomethingNeedsIt(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	counting := http.NewServeMux()
	counting.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]++
		mu.Unlock()
		answeringEngine().ServeHTTP(w, r)
	})
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: counting}
	go server.Serve(listener)
	defer server.Close()

	c, err := Dial(context.Background(), Target{Host: "unix://" + socket})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mu.Lock()
	if asked["info"]+asked["version"] != 0 {
		t.Errorf("dialing asked %v: the facts are for whoever needs them", asked)
	}
	mu.Unlock()
	for range 2 {
		if !c.IsRootless() {
			t.Error("rootless was not read")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if asked["info"] != 1 || asked["version"] != 0 {
		t.Errorf("after IsRootless twice the engine was asked %v: want /info once and no /version", asked)
	}
}

func TestDialIsRefusedWhenNothingAnswers(t *testing.T) {
	_, err := Dial(context.Background(), Target{Host: "unix://" + filepath.Join(t.TempDir(), "none.sock")})
	if err == nil || !strings.Contains(err.Error(), "no container engine reachable") {
		t.Errorf("err = %v", err)
	}
	if _, err := Dial(context.Background(), Target{}); err == nil {
		t.Error("an empty host was accepted")
	}
}

func authority(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func leaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "leaf"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage},
	}
	der, _ := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestDialTCPWithMutualTLS(t *testing.T) {
	ca, caKey, caPEM := authority(t)
	serverCert, serverKey := leaf(t, ca, caKey, 2, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := leaf(t, ca, caKey, 3, x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	server := httptest.NewUnstartedServer(answeringEngine())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	defer server.Close()
	host := "tcp://" + strings.TrimPrefix(server.URL, "https://")

	c, err := Dial(context.Background(), Target{Host: host, CA: caPEM, Cert: clientCert, Key: clientKey})
	if err != nil {
		t.Fatalf("with the right credentials: %v", err)
	}
	c.Close()

	if _, err := Dial(context.Background(), Target{Host: host, CA: caPEM}); err == nil {
		t.Error("connected without the client certificate the engine requires")
	}
	_, _, otherCA := authority(t)
	if _, err := Dial(context.Background(), Target{Host: host, CA: otherCA, Cert: clientCert, Key: clientKey}); err == nil {
		t.Error("trusted an engine whose certificate another CA signed")
	}
	if _, err := Dial(context.Background(), Target{Host: host, CA: []byte("not pem")}); err == nil {
		t.Error("accepted a CA that is not PEM")
	}
}

func TestDialUsesTheDialerItIsGiven(t *testing.T) {
	server := httptest.NewServer(answeringEngine())
	defer server.Close()
	var dialed string
	c, err := Dial(context.Background(), Target{
		Host: "tcp://engine.invalid:2375",
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = addr
			return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if dialed != "engine.invalid:2375" {
		t.Errorf("dialed %q", dialed)
	}
}

// Each command is a new process, so what the engine said is kept for as long as its socket is the same file.
func TestWhatTheEngineIsIsRememberedForAsLongAsItsSocketIsTheSameFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	infos := 0
	counting := http.NewServeMux()
	counting.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info") {
			mu.Lock()
			infos++
			mu.Unlock()
		}
		answeringEngine().ServeHTTP(w, r)
	})
	socket := filepath.Join(t.TempDir(), "engine.sock")
	serve := func() *http.Server {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: counting}
		go server.Serve(listener)
		return server
	}
	rootless := func() bool {
		t.Setenv("DOCKER_HOST", "unix://"+socket)
		c, err := Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.IsRootless()
	}

	server := serve()
	for range 3 { // three commands
		if !rootless() {
			t.Fatal("rootless was not read")
		}
	}
	mu.Lock()
	if infos != 1 {
		t.Errorf("the engine was asked %d times by three commands, want once", infos)
	}
	mu.Unlock()

	server.Close() // the engine restarts: a new socket file
	os.Remove(socket)
	server = serve()
	defer server.Close()
	if !rootless() {
		t.Fatal("rootless was not read after the restart")
	}
	mu.Lock()
	defer mu.Unlock()
	if infos != 2 {
		t.Errorf("after the socket was replaced the engine was asked %d times in all, want 2", infos)
	}
}

// Setting up the engine is the user's job, whatever their init system: egzo says where it looked and how to point it elsewhere.
func TestAnEngineThatDoesNotAnswerIsNotMetWithAdviceOnHowToStartIt(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "none.sock"))
	_, err := Connect(context.Background())
	if err == nil {
		t.Fatal("an engine that does not exist answered")
	}
	for _, word := range []string{"systemctl", "systemd", "enable --now"} {
		if strings.Contains(err.Error(), word) {
			t.Errorf("the error tells the user how to start an engine (%q): %v", word, err)
		}
	}
	if !strings.Contains(err.Error(), "DOCKER_HOST") {
		t.Errorf("the error does not say that DOCKER_HOST chooses the engine: %v", err)
	}
}
