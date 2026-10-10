// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failOnce fails the first import and lets the rest through, as certutil does once the database is new.
type failOnce struct {
	recorder
	failed bool
}

func (f *failOnce) run(name string, args ...string) ([]byte, error) {
	if args[2] == "-A" && !f.failed {
		f.failed = true
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		return []byte("SEC_ERROR_BAD_DATABASE"), os.ErrInvalid
	}
	return f.recorder.run(name, args...)
}

func TestADatabaseThatCannotTakeTheCAIsSetAsideAndRecreated(t *testing.T) {
	home, ca := t.TempDir(), caFile(t)
	directory := filepath.Join(home, ".pki", "nssdb")
	os.MkdirAll(directory, 0o700)
	os.WriteFile(filepath.Join(directory, "cert9.db"), []byte("junk"), 0o600)
	// an older copy from an earlier recovery is not kept next to the new one
	older := directory + ".broken-1"
	os.MkdirAll(older, 0o700)

	var r failOnce
	if err := TrustProjectCA(home, ca, found, r.run); err != nil {
		t.Fatalf("the recovery failed: %v", err)
	}
	set, _ := filepath.Glob(directory + ".broken-*")
	if len(set) != 1 || set[0] == older {
		t.Fatalf("broken copies = %v, want exactly the new one", set)
	}
	if data, _ := os.ReadFile(filepath.Join(set[0], "cert9.db")); string(data) != "junk" {
		t.Errorf("the broken database was not kept as it was: %q", data)
	}
	if _, err := os.Stat(filepath.Join(directory, "cert9.db")); err != nil {
		t.Errorf("no new database: %v", err)
	}
	last := r.calls[len(r.calls)-1]
	if !strings.Contains(last, " -A ") || !strings.Contains(strings.Join(r.calls, "\n"), " -N --empty-password") {
		t.Errorf("calls = %v", r.calls)
	}
}

func TestARecoveryThatFailsToo(t *testing.T) {
	r := recorder{fail: map[string]bool{"-A": true}}
	home := t.TempDir()
	err := TrustProjectCA(home, caFile(t), found, r.run)
	if err == nil || !strings.Contains(err.Error(), "it failed") {
		t.Errorf("err = %v", err)
	}
	if set, _ := filepath.Glob(filepath.Join(home, ".pki", "nssdb.broken-*")); len(set) != 1 {
		t.Errorf("broken copies = %v", set)
	}
}

func authority(t *testing.T, name string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// listed returns the certutil -L listing of the home's database.
func listed(t *testing.T, home string) string {
	t.Helper()
	out, err := exec.Command("certutil", "-d", "sql:"+filepath.Join(home, ".pki", "nssdb"), "-L").CombinedOutput()
	if err != nil {
		t.Fatalf("certutil -L: %v: %s", err, out)
	}
	return string(out)
}

func TestWithRealCertutilARotatedCAReplacesTheOldOneAndACorruptDatabaseRecovers(t *testing.T) {
	if _, err := exec.LookPath("certutil"); err != nil {
		t.Skip("certutil is not installed")
	}
	home := t.TempDir()
	a, b := authority(t, "egzo test CA A"), authority(t, "egzo test CA B")
	look := func(name string) (string, error) { return exec.LookPath(name) }

	if err := TrustProjectCA(home, a, look, ExecRunner); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, home); !strings.Contains(got, caNickname) {
		t.Fatalf("CA A is not trusted:\n%s", got)
	}
	if err := TrustProjectCA(home, b, look, ExecRunner); err != nil {
		t.Fatal(err)
	}
	listing := listed(t, home)
	if strings.Count(listing, caNickname) != 1 {
		t.Errorf("want exactly one certificate:\n%s", listing)
	}
	subject, err := exec.Command("certutil", "-d", "sql:"+filepath.Join(home, ".pki", "nssdb"), "-L", "-n", caNickname).CombinedOutput()
	if err != nil || !strings.Contains(string(subject), "egzo test CA B") || strings.Contains(string(subject), "egzo test CA A") {
		t.Errorf("the certificate is not B's (%v):\n%s", err, subject)
	}

	// a junk database
	if err := os.WriteFile(filepath.Join(home, ".pki", "nssdb", "cert9.db"), []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrustProjectCA(home, a, look, ExecRunner); err != nil {
		t.Fatalf("no recovery from a corrupt database: %v", err)
	}
	if got := listed(t, home); strings.Count(got, caNickname) != 1 {
		t.Errorf("after the recovery:\n%s", got)
	}
	if set, _ := filepath.Glob(filepath.Join(home, ".pki", "nssdb.broken-*")); len(set) != 1 {
		t.Errorf("broken copies = %v", set)
	}
}
