// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProjectCA is where the agent container has the project CA (internal/stack mounts it; duplicated here so
// the agent side does not import the stack, which is the CLI's side of the engine).
const ProjectCA = "/etc/egzo/ca/ca.crt"

// caNickname names the project CA in the NSS database.
const caNickname = "egzo-project-ca"

// keptBackups is how many set-aside databases stay next to the new one.
const keptBackups = 2

// stderr is where the notes the user should see go: a database set aside is not an error, but it is a
// file they may want.
var stderr io.Writer = os.Stderr

// Runner runs a program and returns its combined output.
type Runner func(name string, args ...string) ([]byte, error)

// ExecRunner runs the program for real.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// TrustProjectCA puts the project CA in the NSS database of the agent's home, which is where Chromium looks
// for the authorities it trusts (it ignores SSL_CERT_FILE). The proxy terminates TLS with this CA for what a
// browser sends (Encrypted Client Hello), so a browser without it fails every such page.
//
// It does nothing when `certutil` is not on the PATH (an image without a browser), and is idempotent: the
// certificate is replaced, not added again, so the database follows `egzo ca rotate`. An error is for the
// caller to print; it must never keep the harness from starting.
//
// A database certutil cannot even list is damaged: it is set aside (the newest two copies are kept, and the
// path is printed) and a new one is started. A failure to import into a readable database is only
// reported: the cause is the certificate, the disk or the permissions, and the database is not to blame.
func TrustProjectCA(home, caFile string, lookPath func(string) (string, error), run Runner) error {
	certutil, err := lookPath("certutil")
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil // no project CA mounted: not an egzo-managed network
	}
	// A certificate that does not parse (the file is being rotated) is no reason to touch the database.
	if err := checkCertificate(data); err != nil {
		fmt.Fprintf(stderr, "egzo: %s is not a usable certificate (%v): the browser is not set to trust the project CA\n", caFile, err)
		return nil
	}
	directory := filepath.Join(home, ".pki", "nssdb")
	database := "sql:" + directory
	step := func(args ...string) error {
		if output, err := run(certutil, append([]string{"-d", database}, args...)...); err != nil {
			return fmt.Errorf("certutil %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	exists := func() bool {
		_, err := os.Stat(filepath.Join(directory, "cert9.db"))
		return err == nil
	}

	// open leaves a database certutil can use, and says whether the one it found was damaged.
	open := func() (damaged bool, err error) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return false, err
		}
		if !exists() {
			err := step("-N", "--empty-password")
			return err != nil && exists(), err // a half-made database is damaged; no database at all is not
		}
		err = step("-L")
		return err != nil, err
	}
	damaged, err := open()
	if damaged {
		backup, moveErr := setAside(directory)
		if moveErr != nil {
			return fmt.Errorf("the NSS database is damaged (%v) and could not be set aside: %w", err, moveErr)
		}
		fmt.Fprintf(stderr, "egzo: the browser's certificate database was damaged (%v): kept as %s, started a new one\n", err, backup)
		if _, err = open(); err != nil {
			return fmt.Errorf("%w (after setting the damaged database aside as %s)", err, backup)
		}
	} else if err != nil {
		return err
	}
	// The nickname is unique in a database: remove the old certificate (there is none the first time) so a
	// rotated CA replaces it instead of failing to be added.
	_ = step("-D", "-n", caNickname)
	return step("-A", "-t", "C,,", "-n", caNickname, "-i", caFile)
}

// checkCertificate says why data is not a PEM certificate.
func checkCertificate(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("no PEM certificate")
	}
	_, err := x509.ParseCertificate(block.Bytes)
	return err
}

// setAside moves directory to a new, unique name next to it and returns that name. Only once that worked
// are the oldest copies beyond keptBackups removed, so a failure never costs a database.
func setAside(directory string) (string, error) {
	backup := fmt.Sprintf("%s.broken-%019d", directory, time.Now().UnixNano())
	if err := os.Rename(directory, backup); err != nil {
		return "", err
	}
	old, _ := filepath.Glob(directory + ".broken-*")
	sort.Strings(old)
	for len(old) > keptBackups {
		os.RemoveAll(old[0])
		old = old[1:]
	}
	return backup, nil
}
