// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct {
	calls []string
	fail  map[string]bool // by certutil option, for example "-A"
}

func (r *recorder) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if r.fail[args[2]] {
		return []byte("it failed"), errors.New("exit status 255")
	}
	if args[2] == "-N" { // what certutil does: it creates the database
		dir := strings.TrimPrefix(args[1], "sql:")
		os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600)
	}
	return nil, nil
}

func found(string) (string, error)   { return "/usr/bin/certutil", nil }
func missing(string) (string, error) { return "", os.ErrNotExist }

func caFile(t *testing.T) string {
	path := filepath.Join(t.TempDir(), "ca.crt")
	os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----"), 0o644)
	return path
}

func TestTheFirstStartCreatesTheDatabaseAndTrustsTheCAForTLS(t *testing.T) {
	home, ca := t.TempDir(), caFile(t)
	var r recorder
	if err := TrustProjectCA(home, ca, found, r.run); err != nil {
		t.Fatal(err)
	}
	db := "sql:" + filepath.Join(home, ".pki", "nssdb")
	want := []string{
		"/usr/bin/certutil -d " + db + " -N --empty-password",
		"/usr/bin/certutil -d " + db + " -D -n egzo-project-ca",
		"/usr/bin/certutil -d " + db + " -A -t C,, -n egzo-project-ca -i " + ca,
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(r.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestAnotherStartReplacesTheCertificateWithoutCreatingTheDatabaseOrAccumulating(t *testing.T) {
	home, ca := t.TempDir(), caFile(t)
	var r recorder
	TrustProjectCA(home, ca, found, r.run)
	r.calls = nil
	if err := TrustProjectCA(home, ca, found, r.run); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || !strings.Contains(r.calls[0], " -D -n egzo-project-ca") || !strings.Contains(r.calls[1], " -A ") {
		t.Errorf("calls = %v", r.calls)
	}
}

func TestWithoutCertutilOrWithoutACAThereIsNothingToDo(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := TrustProjectCA(home, caFile(t), missing, r.run); err != nil || len(r.calls) != 0 {
		t.Errorf("no certutil: err %v, calls %v", err, r.calls)
	}
	if err := TrustProjectCA(home, filepath.Join(home, "no-ca.crt"), found, r.run); err != nil || len(r.calls) != 0 {
		t.Errorf("no CA: err %v, calls %v", err, r.calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".pki")); !os.IsNotExist(err) {
		t.Error("a database was made for nothing")
	}
}

func TestAFailureIsReturnedWithWhatCertutilSaidAndAFailedRemovalIsIgnored(t *testing.T) {
	r := recorder{fail: map[string]bool{"-A": true}}
	err := TrustProjectCA(t.TempDir(), caFile(t), found, r.run)
	if err == nil || !strings.Contains(err.Error(), "it failed") {
		t.Errorf("err = %v", err)
	}
	r = recorder{fail: map[string]bool{"-D": true}} // nothing to remove the first time
	if err := TrustProjectCA(t.TempDir(), caFile(t), found, r.run); err != nil {
		t.Errorf("a failed removal is not an error: %v", err)
	}
	r = recorder{fail: map[string]bool{"-N": true}}
	if err := TrustProjectCA(t.TempDir(), caFile(t), found, r.run); err == nil || len(r.calls) != 1 {
		t.Errorf("a database that cannot be created stops there: %v, %v", err, r.calls)
	}
}
