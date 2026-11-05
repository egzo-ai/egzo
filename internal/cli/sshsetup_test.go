// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/egzo-ai/egzo/internal/proxy"
)

func TestSetupSSHWritesTheIdentityTheProxyBinds(t *testing.T) {
	home := t.TempDir()
	if err := setupSSH(home, "a-token", ""); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(filepath.Join(home, ".ssh", proxy.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	bound, _ := proxy.IdentityPublicKey("a-token")
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); got != bound {
		t.Fatalf("the instance signs with %q, the proxy binds %q", got, bound)
	}
	info, _ := os.Stat(filepath.Join(home, ".ssh", proxy.IdentityFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestSetupSSHPutsThePublishedServersFirstAndReplacesThemAtEachStart(t *testing.T) {
	home, published := t.TempDir(), filepath.Join(t.TempDir(), "ssh_config")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host mine\n  User me\n"), 0o600)
	os.WriteFile(published, []byte("Host prod\n    User prod\n"), 0o644)
	for i := 0; i < 2; i++ {
		if err := setupSSH(home, "t", published); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	want := managedBegin + "Host prod\n    User prod\n" + managedEnd + "Host mine\n  User me\n"
	if string(data) != want {
		t.Fatalf("config = %q", data)
	}
	// The servers changed, and one went away altogether: the block follows, the person's own lines stay.
	os.WriteFile(published, []byte("Host db\n"), 0o644)
	setupSSH(home, "t", published)
	data, _ = os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if string(data) != managedBegin+"Host db\n"+managedEnd+"Host mine\n  User me\n" {
		t.Fatalf("config = %q", data)
	}
	os.WriteFile(published, nil, 0o644)
	setupSSH(home, "t", published)
	data, _ = os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if string(data) != "Host mine\n  User me\n" {
		t.Fatalf("config = %q", data)
	}
	if info, _ := os.Stat(filepath.Join(home, ".ssh", "config")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestSetupSSHDoesNothingWithoutAToken(t *testing.T) {
	home := t.TempDir()
	if err := setupSSH(home, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); err == nil {
		t.Fatal("wrote something")
	}
}

func TestARunningAgentFollowsTheServersTheProxyPublishes(t *testing.T) {
	home, shared := t.TempDir(), t.TempDir()
	published := filepath.Join(shared, "ssh_config")
	os.WriteFile(published, []byte("Host prod\n    HostName proxy\n"), 0o644)
	if err := setupSSH(home, "a-token", published); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".ssh", "config")
	read := func() string { data, _ := os.ReadFile(config); return string(data) }
	waitFor := func(what string, ok func(string) bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if ok(read()) {
				return
			}
		}
		t.Fatalf("%s; the config is:\n%s", what, read())
	}
	done := make(chan struct{})
	defer close(done)
	go followSSH(home, "a-token", published, 10*time.Millisecond, done)

	os.WriteFile(published, []byte("Host prod\n    HostName proxy\nHost staging\n    HostName proxy\n"), 0o644)
	waitFor("a server the proxy published since is in the config", func(c string) bool { return strings.Contains(c, "Host staging") })
	os.WriteFile(published, []byte("Host staging\n    HostName proxy\n"), 0o644)
	waitFor("a server the proxy no longer publishes leaves it", func(c string) bool {
		return !strings.Contains(c, "Host prod") && strings.Contains(c, "Host staging")
	})
	if strings.Count(read(), managedBegin) != 1 {
		t.Fatalf("one managed block, rewritten in place:\n%s", read())
	}
}
