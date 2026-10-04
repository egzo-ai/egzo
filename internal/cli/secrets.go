package cli

import (
	"bufio"
	"fmt"
	"io"
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
				state := "set"
				if _, err := stack.ReadSecret(source, p.Dir); err != nil {
					state = "missing"
				}
				fmt.Fprintf(table, "%s\t%s\t%s\n", ref, source, state)
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
			if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
				return err
			}
			os.Chmod(path, 0o600)
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
	line, err := bufio.NewReader(io.LimitReader(in, 1<<20)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
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
