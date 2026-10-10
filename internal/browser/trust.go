// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectCA is where the agent container has the project CA (internal/stack mounts it; duplicated here so
// the agent side does not import the stack, which is the CLI's side of the engine).
const ProjectCA = "/etc/egzo/ca/ca.crt"

// caNickname names the project CA in the NSS database.
const caNickname = "egzo-project-ca"

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
func TrustProjectCA(home, caFile string, lookPath func(string) (string, error), run Runner) error {
	certutil, err := lookPath("certutil")
	if err != nil {
		return nil
	}
	if _, err := os.Stat(caFile); err != nil {
		return nil // no project CA mounted: not an egzo-managed network
	}
	directory := filepath.Join(home, ".pki", "nssdb")
	database := "sql:" + directory
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	step := func(args ...string) error {
		if output, err := run(certutil, append([]string{"-d", database}, args...)...); err != nil {
			return fmt.Errorf("certutil %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(directory, "cert9.db")); err != nil {
		if err := step("-N", "--empty-password"); err != nil {
			return err
		}
	}
	// The nickname is unique in a database: remove the old certificate (there is none the first time) so a
	// rotated CA replaces it instead of failing to be added.
	_ = step("-D", "-n", caNickname)
	return step("-A", "-t", "C,,", "-n", caNickname, "-i", caFile)
}
