// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
)

// ErrEmptySecret is wrapped by the error for a secret that holds nothing.
var ErrEmptySecret = errors.New("empty secret")

// ErrPassNotFound is wrapped by the error for a pass vault when `pass` is not on PATH.
var ErrPassNotFound = errors.New("`pass` was not found on PATH")

// ReadSecret reads the value of a secret from its backend. dir is the project directory.
// An error never contains a value.
func ReadSecret(ref config.SecretRef, dir string) (string, error) {
	switch ref.Backend {
	case "env":
		value, ok := os.LookupEnv(ref.Name)
		if !ok || value == "" {
			return "", fmt.Errorf("environment variable %s is not set", ref.Name)
		}
		return value, nil
	case "pass":
		return readPass(ref.Name)
	}
	return "", fmt.Errorf("unknown backend %q", ref.Backend)
}

// ErrNotASecret is wrapped by the error for a pass name that is a directory of the store.
var ErrNotASecret = errors.New("not a secret")

// PassError is a failure of `pass show` itself; Message is the first line pass printed on stderr.
type PassError struct{ Message string }

func (e *PassError) Error() string { return "pass: " + e.Message }

// readPass runs `pass show <name>` with the environment, stdin and terminal of egzo untouched (so gpg-agent can
// prompt) and returns the first line of its output; the other lines are notes. There is no timeout: a pinentry
// prompt may wait for the user.
func readPass(name string) (string, error) {
	path, err := exec.LookPath("pass")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPassNotFound, err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(path, "show", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", fmt.Errorf("cannot run `pass`: %w", err)
		}
		message := strings.TrimSpace(stderr.String())
		message, _, _ = strings.Cut(message, "\n")
		if message == "" {
			message = exit.Error()
		}
		return "", &PassError{Message: message}
	}
	if isPassTree(stdout.String()) {
		return "", fmt.Errorf("%w: %s is a directory in the password store, not a secret", ErrNotASecret, name)
	}
	first, _, _ := strings.Cut(stdout.String(), "\n")
	first = strings.TrimRight(first, "\r")
	if first == "" {
		return "", fmt.Errorf("%w: pass entry %s has an empty first line", ErrEmptySecret, name)
	}
	return first, nil
}

// isPassTree recognizes what `pass show` prints for a directory: the name, then a tree. Names cannot start
// with '-' (checked when the configuration is loaded), so pass never reads one as an option.
func isPassTree(output string) bool {
	lines := strings.Split(output, "\n")
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "├── ") || strings.HasPrefix(line, "└── ") {
			return true
		}
	}
	return false
}
