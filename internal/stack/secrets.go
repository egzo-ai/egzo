// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/template"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/seal"
)

// ErrEmptySecret is wrapped by the error for a secret that holds nothing.
var ErrEmptySecret = errors.New("empty secret")

// ErrPassNotFound is wrapped by the error for a pass vault when `pass` is not on PATH.
var ErrPassNotFound = errors.New("`pass` was not found on PATH")

// ReadSecret reads the value of a secret from its backend. dir is the project directory, where the env backend finds
// the project's .env.
// An error never contains a value.
func ReadSecret(ref config.SecretRef, dir string) (string, error) {
	return readSecret(ref, dir, false)
}

// ReadSecretText is ReadSecret for a secret that is text of several lines, such as an ssh private key: a pass entry is
// read whole instead of by its first line (the other lines are notes for any other secret).
func ReadSecretText(ref config.SecretRef, dir string) (string, error) {
	return readSecret(ref, dir, true)
}

// ShortSecretKeyError is a project secret key that is too short to be one. It never holds the value.
type ShortSecretKeyError struct{ Bytes int }

func (e *ShortSecretKeyError) Error() string {
	return fmt.Sprintf("too short: %d bytes, a secret key is at least %d bytes (make one with `openssl rand -hex 32`)", e.Bytes, seal.MinKeyBytes)
}

// ReadSecretKey reads the project's secret key: a secret like any other, at least seal.MinKeyBytes long.
func ReadSecretKey(ref config.SecretRef, dir string) (string, error) {
	value, err := ReadSecret(ref, dir)
	if err != nil {
		return "", err
	}
	if len(value) < seal.MinKeyBytes {
		return "", &ShortSecretKeyError{Bytes: len(value)}
	}
	return value, nil
}

func readSecret(ref config.SecretRef, dir string, whole bool) (string, error) {
	switch ref.Backend {
	case "env":
		return readEnv(ref.Name, dir)
	case "pass":
		return readPass(ref.Name, whole)
	}
	return "", fmt.Errorf("unknown backend %q", ref.Backend)
}

// DotenvFile is the file of the project directory the env backend reads a variable from when the environment does not
// have it, as Docker Compose does.
const DotenvFile = ".env"

// readEnv reads a variable from the environment or, when the environment does not have it, from the project's .env.
// Compose's order: a variable of the environment wins, even set to nothing.
func readEnv(name, dir string) (string, error) {
	if value, ok := os.LookupEnv(name); ok {
		if value != "" {
			return value, nil
		}
		if vars, err := readDotenv(dir); err == nil && vars[name] != "" {
			return "", fmt.Errorf("environment variable %s is not set: it is empty, which hides the one in %s", name, DotenvFile)
		}
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	vars, err := readDotenv(dir)
	if err != nil {
		return "", err
	}
	value, ok := vars[name]
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	if value == "" {
		return "", fmt.Errorf("%w: %s is empty in %s", ErrEmptySecret, name, DotenvFile)
	}
	return value, nil
}

// FromDotenv tells whether the env backend reads ref from the project's .env rather than the environment.
func FromDotenv(ref config.SecretRef, dir string) bool {
	if ref.Backend != "env" {
		return false
	}
	if _, ok := os.LookupEnv(ref.Name); ok {
		return false
	}
	vars, err := readDotenv(dir)
	_, ok := vars[ref.Name]
	return err == nil && ok
}

// DotenvError is a .env that cannot be parsed. It never holds a value: the parser's own messages can quote one.
type DotenvError struct {
	Line   int    // 0 when the parser does not say
	Reason string // a message of egzo's, or empty
}

func (e *DotenvError) Error() string {
	message := DotenvFile + " cannot be read"
	if e.Line > 0 {
		message = fmt.Sprintf("%s: line %d is not valid", DotenvFile, e.Line)
	}
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

var dotenvLine = regexp.MustCompile(`^line (\d+):`)

// readDotenv parses the project's .env with Compose's own parser, ${VAR} taken from the environment first, then
// from the lines above. No file, or a directory by that name, is no variable.
func readDotenv(dir string) (map[string]string, error) {
	path := filepath.Join(dir, DotenvFile)
	if info, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || err == nil && info.IsDir() {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	vars, err := dotenv.ParseWithLookup(file, os.LookupEnv)
	if err == nil {
		return vars, nil
	}
	problem := &DotenvError{}
	var missing *template.MissingRequiredError
	var invalid *template.InvalidTemplateError
	switch {
	case errors.As(err, &missing):
		problem.Reason = "required variable " + missing.Variable + " is missing a value"
	case errors.As(err, &invalid):
		problem.Reason = "a ${...} in a value is not valid"
	default:
		if m := dotenvLine.FindStringSubmatch(err.Error()); m != nil {
			problem.Line, _ = strconv.Atoi(m[1])
		}
	}
	return nil, problem
}

// ErrNotASecret is wrapped by the error for a pass name that is a directory of the store.
var ErrNotASecret = errors.New("not a secret")

// PassError is a failure of `pass show` itself; Message is the first line pass printed on stderr.
type PassError struct{ Message string }

func (e *PassError) Error() string { return "pass: " + e.Message }

// readPass runs `pass show <name>` with the environment, stdin and terminal of egzo untouched (so gpg-agent can
// prompt) and returns the first line of its output; the other lines are notes. There is no timeout: a pinentry
// prompt may wait for the user.
func readPass(name string, whole bool) (string, error) {
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
	if whole {
		text := strings.TrimRight(stdout.String(), "\r\n")
		if text == "" {
			return "", fmt.Errorf("%w: pass entry %s is empty", ErrEmptySecret, name)
		}
		return text + "\n", nil
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
