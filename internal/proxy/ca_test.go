package proxy

import (
	"crypto/x509"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCAPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Error("the CA changed between loads: agents would have to be restarted to trust it again")
	}
	info, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("CA key mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestPublishNeverWritesTheKey(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	system := filepath.Join(t.TempDir(), "system.crt")
	os.WriteFile(system, []byte("-----SYSTEM ROOTS-----\n"), 0o644)
	pub := t.TempDir()
	if err := ca.Publish(pub, system); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(pub)
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(pub, entry.Name()))
		if strings.Contains(string(data), "PRIVATE KEY") {
			t.Errorf("%s contains a private key", entry.Name())
		}
	}
	bundle, _ := os.ReadFile(filepath.Join(pub, "ca-bundle.crt"))
	if !strings.Contains(string(bundle), "SYSTEM ROOTS") || !strings.Contains(string(bundle), "BEGIN CERTIFICATE") {
		t.Errorf("bundle should hold the system roots and the project CA:\n%s", bundle)
	}
}

func TestPublishWorksWithoutASystemBundle(t *testing.T) {
	ca, _ := LoadOrCreateCA(t.TempDir())
	if err := ca.Publish(t.TempDir(), "/nonexistent/ca-certificates.crt"); err != nil {
		t.Fatal(err)
	}
}

func TestLeafCertificatesVerifyAgainstTheCA(t *testing.T) {
	ca, _ := LoadOrCreateCA(t.TempDir())
	minter := NewMinter(ca)
	certificate, err := minter.Certificate("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	if _, err := certificate.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "api.example.com"}); err != nil {
		t.Errorf("leaf does not verify: %v", err)
	}
	if _, err := certificate.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.example.com"}); err == nil {
		t.Error("leaf is valid for a host it was not minted for")
	}
	if lifetime := certificate.Leaf.NotAfter.Sub(certificate.Leaf.NotBefore); lifetime > 25*time.Hour {
		t.Errorf("leaf lifetime = %v, want short-lived", lifetime)
	}
}

func TestLeafCertificatesAreCachedAndRenewed(t *testing.T) {
	ca, _ := LoadOrCreateCA(t.TempDir())
	minter := NewMinter(ca)
	first, _ := minter.Certificate("a.example.com")
	again, _ := minter.Certificate("a.example.com")
	if first != again {
		t.Error("certificate was not cached")
	}
	now := time.Now().Add(23*time.Hour + 30*time.Minute)
	minter.now = func() time.Time { return now }
	renewed, _ := minter.Certificate("a.example.com")
	if renewed == first {
		t.Error("a certificate about to expire was reused")
	}
}

func TestPolicyDecisions(t *testing.T) {
	agent := Profile{
		Allow: []string{"*.pypi.org", "example.com"},
		Services: []Service{
			{Name: "anthropic", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Secret: "KEY"},
			{Name: "github", Hosts: []string{"api.github.com"}, Header: "Authorization", Value: "Bearer {secret}", Secret: "TOK"},
			{Name: "pypi", Hosts: []string{"pypi.org"}},
		},
	}
	if d := agent.Decide("api.anthropic.com"); !d.Allowed || d.Inject == nil || d.Inject.Header != "x-api-key" || d.Inject.Value != "KEY" {
		t.Errorf("anthropic decision = %+v", d)
	}
	if d := agent.Decide("API.GitHub.com"); !d.Allowed || d.Inject == nil || d.Inject.Value != "Bearer TOK" {
		t.Errorf("github decision = %+v", d)
	}
	if d := agent.Decide("pypi.org"); !d.Allowed || d.Inject != nil {
		t.Errorf("pure allowlist service must not inject: %+v", d)
	}
	if d := agent.Decide("files.pypi.org"); !d.Allowed || d.Inject != nil {
		t.Errorf("allow glob decision = %+v", d)
	}
	if d := agent.Decide("evil.example"); d.Allowed {
		t.Errorf("unlisted host allowed: %+v", d)
	}
	everything := Profile{Allow: []string{"*"}}
	if d := everything.Decide("anything.example"); !d.Allowed || d.Inject != nil {
		t.Errorf("allow * decision = %+v", d)
	}
}

func TestAuthenticateNeedsTheRightTokenForTheRightAgent(t *testing.T) {
	server := NewServer(nil, NewAudit(io.Discard))
	policy := &Policy{Hash: "h", Profiles: map[string]Profile{"p": {Allow: []string{"example.com"}}}}
	server.SetPolicy(policy)
	for name, token := range map[string]string{"coder": "tc", "reviewer": "tr"} {
		if err := server.BindAgent(name, Binding{Token: token, Profile: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if server.authenticate(policy, "coder", "tc") == nil {
		t.Error("valid credentials refused")
	}
	if server.authenticate(policy, "coder", "tr") != nil {
		t.Error("another agent's token accepted")
	}
	if server.authenticate(policy, "ghost", "tc") != nil {
		t.Error("unknown agent accepted")
	}
	if server.authenticate(policy, "coder", "") != nil {
		t.Error("an empty token accepted")
	}
	if err := server.BindAgent("nobody", Binding{Profile: "p"}); err == nil {
		t.Error("a binding without a token was accepted")
	}
}

func TestBindingsNeedALoadedProfileAndFollowItsChanges(t *testing.T) {
	server := NewServer(nil, NewAudit(io.Discard))
	if err := server.BindAgent("coder", Binding{Token: "t", Profile: "p"}); err == nil {
		t.Error("an agent was bound before any policy was loaded")
	}
	first := &Policy{Hash: "1", Profiles: map[string]Profile{"p": {Allow: []string{"a.example"}}}}
	server.SetPolicy(first)
	if err := server.BindAgent("coder", Binding{Token: "t", Profile: "missing"}); err == nil {
		t.Error("an agent was bound to a profile that does not exist")
	}
	if err := server.BindAgent("coder", Binding{Token: "t", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	// Loading a new policy keeps the binding and gives the agent the new profile at once.
	second := &Policy{Hash: "2", Profiles: map[string]Profile{"p": {Allow: []string{"b.example"}}}}
	server.SetPolicy(second)
	profile := server.authenticate(second, "coder", "t")
	if profile == nil || !profile.Decide("b.example").Allowed || profile.Decide("a.example").Allowed {
		t.Errorf("profile after the change = %+v", profile)
	}
	// A policy without the profile leaves the agent bound but denied.
	third := &Policy{Hash: "3", Profiles: map[string]Profile{}}
	server.SetPolicy(third)
	if server.authenticate(third, "coder", "t") != nil {
		t.Error("an agent kept a profile that was removed")
	}
	server.UnbindAgent("coder")
	server.UnbindAgent("never-bound")
	if len(server.BoundAgents()) != 0 {
		t.Errorf("bound agents = %v", server.BoundAgents())
	}
}

func TestRotateCAIssuesANewCAAndOverwritesTheFiles(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := RotateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Fingerprint() == first.Fingerprint() {
		t.Fatal("the CA did not change")
	}
	again, err := LoadOrCreateCA(dir)
	if err != nil || again.Fingerprint() != rotated.Fingerprint() {
		t.Errorf("the rotated CA was not what got stored: %v", err)
	}
}

func TestTheMinterSignsWithTheNewCAAfterARotationAndForgetsTheOldLeaves(t *testing.T) {
	old, _ := LoadOrCreateCA(t.TempDir())
	minter := NewMinter(old)
	before, err := minter.Certificate("example.test")
	if err != nil {
		t.Fatal(err)
	}
	rotated, _ := RotateCA(t.TempDir())
	minter.SetCA(rotated)
	after, err := minter.Certificate("example.test")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("the cached certificate of the old CA was reused")
	}
	pool := x509.NewCertPool()
	pool.AddCert(rotated.Cert)
	if _, err := after.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.test"}); err != nil {
		t.Errorf("the new leaf does not verify against the new CA: %v", err)
	}
}
