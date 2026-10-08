// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/egzo-ai/egzo/internal/stack"
)

func newSecretsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "See and change the secrets of the project's vaults, never their values",
		Long: "Secrets live in the vaults of egzo.yaml and reach only the proxy. These commands show which are set\n" +
			"and change those stored in a file; they never print a value. A secret read from an environment\n" +
			"variable is changed by changing the variable.",
	}
	cmd.AddCommand(newSecretsLsCommand(opts), newSecretsSetCommand(opts), newSecretsRmCommand(opts))
	return cmd
}

func sortedSecretRefs(p *project) []string {
	refs := make([]string, 0, len(p.Resolved.SecretSources))
	for ref := range p.Resolved.SecretSources {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func newSecretsLsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the secrets and whether each is set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "SECRET\tSOURCE\tSTATE")
			for _, ref := range sortedSecretRefs(p) {
				source := p.Resolved.SecretSources[ref]
				fmt.Fprintf(table, "%s\t%s\t%s\n", ref, source, secretState(source, p.Dir))
			}
			return table.Flush()
		},
	}
}

// fileSecret finds the file behind a secret, or says why it cannot be changed from here.
func fileSecret(p *project, ref string) (string, error) {
	source, ok := p.Resolved.SecretSources[ref]
	if !ok {
		return "", fmt.Errorf("no secret %q in the vaults (secrets: %s)", ref, strings.Join(sortedSecretRefs(p), ", "))
	}
	scheme, location, _ := strings.Cut(source, ":")
	if scheme != "file" {
		return "", fmt.Errorf("secret %s is read from the environment variable %s, and egzo cannot change a variable of your shell: "+
			"set it there, or point the secret at a file with from: file:<path>", ref, location)
	}
	return stack.SecretFilePath(location, p.Dir), nil
}

func newSecretsSetCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "set VAULT/SECRET",
		Short: "Store a secret's value in its file, read from stdin (or typed, hidden)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			path, err := fileSecret(p, args[0])
			if err != nil {
				return err
			}
			value, err := readValue(cmd)
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("the value is empty")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := storeSecret(path, value); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored %s in %s; run `egzo up` to give it to the proxy\n", args[0], path)
			return nil
		},
	}
}

func readValue(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "value (hidden): ")
		data, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return strings.TrimSpace(string(data)), err
	}
	return readSecretValue(in)
}

// readSecretValue reads a secret from a stream whole, up to 1 MB: a key in a file has several lines.
// Only the line breaks at the end are not part of it.
func readSecretValue(in io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(in, 1<<20))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// storeSecret writes a secret file that nobody else could ever read: it is written under a name of its
// own with mode 0600 and moved into place, so an older, more open file is never filled with the new
// value, and a symbolic link is refused rather than written through.
func storeSecret(path, value string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: refusing to write a secret through it", path)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(value + "\n"); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// secretState is what `secrets ls` says about a secret, with the reason when it cannot be used.
func secretState(source, dir string) string {
	_, err := stack.ReadSecret(source, dir)
	switch {
	case err == nil:
		return "set"
	case errors.Is(err, fs.ErrPermission):
		return "unreadable (permission denied)"
	case errors.Is(err, stack.ErrEmptySecret):
		return "empty"
	case errors.Is(err, fs.ErrNotExist):
		return "missing"
	case strings.Contains(err.Error(), "is not set"):
		return "missing"
	}
	return "unreadable (" + firstLine(err.Error()) + ")"
}

// secretFileWarnings names the secret files that other users of this machine can read.
func secretFileWarnings(sources map[string]string, dir string) []string {
	var warnings []string
	refs := make([]string, 0, len(sources))
	for ref := range sources {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		scheme, location, _ := strings.Cut(sources[ref], ":")
		if scheme != "file" {
			continue
		}
		path := stack.SecretFilePath(location, dir)
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, fmt.Sprintf("secret %s: %s can be read by other users (mode %v): chmod 600 %s", ref, path, info.Mode().Perm(), path))
		}
	}
	return warnings
}

func newSecretsRmCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "rm VAULT/SECRET",
		Short: "Delete a secret's file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			path, err := fileSecret(p, args[0])
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s (%s)\n", args[0], path)
			return nil
		},
	}
}
