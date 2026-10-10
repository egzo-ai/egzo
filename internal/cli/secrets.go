// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

func newSecretsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "See which secrets of the project's vaults can be read, never their values",
		Long: "Secrets live in the vaults of egzo.yaml and reach only the proxy. These commands show which are set;\n" +
			"they never print a value and egzo never writes a secret.",
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	cmd.AddCommand(newSecretsLsCommand(opts))
	return cmd
}

func sortedSecretRefs(p *project) []string {
	refs := make([]string, 0, len(p.Resolved.Secrets))
	for ref := range p.Resolved.Secrets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func newSecretsLsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the secrets and whether each can be read (set, empty or missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "SECRET\tSTATE")
			for _, ref := range sortedSecretRefs(p) {
				state, err := secretState(p.Resolved.Secrets[ref], p.Dir)
				if err != nil {
					return err
				}
				fmt.Fprintf(table, "%s\t%s\n", ref, state)
			}
			return table.Flush()
		},
	}
}

// secretState is what `secrets ls` says about a secret, with the reason when it cannot be used. The error is
// only for a failure that makes every answer meaningless: `pass` itself cannot be run.
func secretState(ref config.SecretRef, dir string) (string, error) {
	_, err := stack.ReadSecret(ref, dir)
	var passErr *stack.PassError
	switch {
	case err == nil:
		return "set", nil
	case errors.Is(err, stack.ErrPassNotFound):
		return "", err
	case errors.Is(err, stack.ErrEmptySecret):
		return "empty", nil
	case errors.Is(err, stack.ErrNotASecret):
		return "missing (" + firstLine(err.Error()) + ")", nil
	case errors.As(err, &passErr):
		return "missing (" + passErr.Message + ")", nil
	case strings.Contains(err.Error(), "is not set"):
		return "missing", nil
	}
	return "unreadable (" + firstLine(err.Error()) + ")", nil
}
