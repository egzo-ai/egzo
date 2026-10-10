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

// capture collects what the package prints for the user.
func capture(t *testing.T) *strings.Builder {
	t.Helper()
	var out strings.Builder
	previous := stderr
	stderr = &out
	t.Cleanup(func() { stderr = previous })
	return &out
}

// damaged is a database directory with junk in it.
func damaged(t *testing.T, home, junk string) string {
	t.Helper()
	directory := filepath.Join(home, ".pki", "nssdb")
	os.MkdirAll(directory, 0o700)
	os.WriteFile(filepath.Join(directory, "cert9.db"), []byte(junk), 0o600)
	return directory
}

// brokenDatabase fails -L until the database is replaced, as certutil does for a damaged cert9.db.
type brokenDatabase struct {
	recorder
	directory string
}

func (b *brokenDatabase) run(name string, args ...string) ([]byte, error) {
	if args[2] == "-L" {
		if data, _ := os.ReadFile(filepath.Join(b.directory, "cert9.db")); string(data) == "junk" {
			b.calls = append(b.calls, name+" "+strings.Join(args, " "))
			return []byte("SEC_ERROR_BAD_DATABASE"), os.ErrInvalid
		}
	}
	return b.recorder.run(name, args...)
}

func TestADatabaseThatCannotBeListedIsSetAsideAndRecreated(t *testing.T) {
	home, ca := t.TempDir(), caFile(t)
	directory := damaged(t, home, "junk")
	printed := capture(t)

	r := brokenDatabase{directory: directory}
	if err := TrustProjectCA(home, ca, found, r.run); err != nil {
		t.Fatalf("the recovery failed: %v", err)
	}
	set, _ := filepath.Glob(directory + ".broken-*")
	if len(set) != 1 {
		t.Fatalf("broken copies = %v, want one", set)
	}
	if data, _ := os.ReadFile(filepath.Join(set[0], "cert9.db")); string(data) != "junk" {
		t.Errorf("the broken database was not kept as it was: %q", data)
	}
	if !strings.Contains(printed.String(), set[0]) {
		t.Errorf("the backup path was not printed: %q", printed.String())
	}
	if _, err := os.Stat(filepath.Join(directory, "cert9.db")); err != nil {
		t.Errorf("no new database: %v", err)
	}
	if last := r.calls[len(r.calls)-1]; !strings.Contains(last, " -A ") || !strings.Contains(strings.Join(r.calls, "\n"), " -N --empty-password") {
		t.Errorf("calls = %v", r.calls)
	}
}

func TestAnImportThatFailsInAReadableDatabaseOnlyReportsAndKeepsTheDatabase(t *testing.T) {
	home := t.TempDir()
	directory := damaged(t, home, "good")
	printed := capture(t)
	r := recorder{fail: map[string]bool{"-A": true}}
	err := TrustProjectCA(home, caFile(t), found, r.run)
	if err == nil || !strings.Contains(err.Error(), "it failed") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(directory, "cert9.db")); string(data) != "good" {
		t.Errorf("the database was touched: %q", data)
	}
	if set, _ := filepath.Glob(directory + ".broken-*"); len(set) != 0 || printed.Len() != 0 {
		t.Errorf("a database was set aside for an import failure: %v %q", set, printed.String())
	}
}

func TestACertificateThatDoesNotParseLeavesEverythingAlone(t *testing.T) {
	home := t.TempDir()
	directory := damaged(t, home, "good")
	printed := capture(t)
	for name, content := range map[string]string{
		"not PEM": "-----BEGIN CERTIFICATE-----", "empty": "", "wrong block": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"bad DER": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	} {
		path := filepath.Join(t.TempDir(), "ca.crt")
		os.WriteFile(path, []byte(content), 0o644)
		var r recorder
		if err := TrustProjectCA(home, path, found, r.run); err != nil || len(r.calls) != 0 {
			t.Errorf("%s: err %v, calls %v", name, err, r.calls)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(directory, "cert9.db")); string(data) != "good" {
		t.Errorf("the database was touched: %q", data)
	}
	if strings.Count(printed.String(), "\n") != 4 {
		t.Errorf("want one line each:\n%s", printed.String())
	}
}

func TestOnlyTheNewestTwoBackupsAreKeptAndNoneIsRemovedWhenTheMoveFails(t *testing.T) {
	home, ca := t.TempDir(), caFile(t)
	capture(t)
	directory := damaged(t, home, "junk")
	var kept []string
	for i := 0; i < 4; i++ {
		os.MkdirAll(directory, 0o700)
		os.WriteFile(filepath.Join(directory, "cert9.db"), []byte("junk"), 0o600)
		r := brokenDatabase{directory: directory}
		if err := TrustProjectCA(home, ca, found, r.run); err != nil {
			t.Fatal(err)
		}
		set, _ := filepath.Glob(directory + ".broken-*")
		if len(set) > keptBackups {
			t.Fatalf("round %d: %d backups", i, len(set))
		}
		kept = set
	}
	if len(kept) != 2 {
		t.Fatalf("backups = %v", kept)
	}

	// the move cannot happen: a plain file where the backup would go is not it, so make the parent
	// unwritable to rename in
	if os.Getuid() == 0 {
		return
	}
	os.WriteFile(filepath.Join(directory, "cert9.db"), []byte("junk"), 0o600)
	parent := filepath.Dir(directory)
	os.Chmod(parent, 0o500)
	defer os.Chmod(parent, 0o700)
	r := brokenDatabase{directory: directory}
	err := TrustProjectCA(home, ca, found, r.run)
	if err == nil || !strings.Contains(err.Error(), "could not be set aside") {
		t.Errorf("err = %v", err)
	}
	if after, _ := filepath.Glob(directory + ".broken-*"); strings.Join(after, ",") != strings.Join(kept, ",") {
		t.Errorf("backups changed although the move failed: %v -> %v", kept, after)
	}
	if data, _ := os.ReadFile(filepath.Join(directory, "cert9.db")); string(data) != "junk" {
		t.Errorf("the database is gone: %q", data)
	}
}

func TestACreationThatLeavesAHalfMadeDatabaseIsSetAsideButNoDatabaseIsNot(t *testing.T) {
	capture(t)
	home := t.TempDir()
	r := recorder{fail: map[string]bool{"-N": true}}
	if err := TrustProjectCA(home, caFile(t), found, r.run); err == nil {
		t.Error("no error")
	}
	if set, _ := filepath.Glob(filepath.Join(home, ".pki", "nssdb.broken-*")); len(set) != 0 {
		t.Errorf("an empty directory was set aside: %v", set)
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
	capture(t)
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
