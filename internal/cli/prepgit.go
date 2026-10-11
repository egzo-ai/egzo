// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/gitprep"
)

// warningPrefix starts a line of a git job's output that is a warning, not an answer: the CLI shows it.
const warningPrefix = "egzo-warning: "

func newPrepGitCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "git", Short: "Git jobs for git workspaces", Hidden: true}
	var url, branch string

	clone := &cobra.Command{
		Use:   "clone DIR",
		Short: "Clone a repository into DIR, without checking anything out",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return gitprep.Clone(url, branch, args[0]) },
	}
	fetch := &cobra.Command{
		Use:   "fetch DIR",
		Short: "Fetch the configured branch into the base clone in DIR",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return gitprep.Fetch(url, branch, args[0]) },
	}
	var options gitprep.WorktreeOptions
	worktree := &cobra.Command{
		Use:   "worktree DIR",
		Short: "Make DIR a worktree of the base clone, cloning the base first when needed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.URL, options.Branch, options.Dir = url, branch, args[0]
			warnings, err := gitprep.Worktree(options)
			for _, warning := range warnings {
				fmt.Fprintln(cmd.OutOrStdout(), warningPrefix+warning)
			}
			return err
		},
	}
	info := &cobra.Command{
		Use:   "info DIR",
		Short: "Print as JSON what the base clone in DIR is: cloned or not, its origin url, the branches egzo made in it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			found, err := gitprep.Info(args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(found)
		},
	}
	var forgetBase string
	forget := &cobra.Command{
		Use:   "forget INSTANCE...",
		Short: "Make the base clone forget the worktrees of instances whose volumes are gone",
		Args:  cobra.MinimumNArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return gitprep.Forget(forgetBase, args) },
	}
	forget.Flags().StringVar(&forgetBase, "base", "", "the base clone")
	forget.MarkFlagRequired("base")
	for _, c := range []*cobra.Command{clone, worktree, fetch} {
		c.Flags().StringVar(&url, "url", "", "the repository (https)")
		c.Flags().StringVar(&branch, "branch", "", "the branch to start from")
		c.MarkFlagRequired("url")
	}
	worktree.Flags().StringVar(&options.Base, "base", "", "the base clone the worktree belongs to")
	worktree.Flags().StringVar(&options.Agent, "agent", "", "the agent the worktree is for")
	worktree.Flags().BoolVar(&options.NoFetch, "no-fetch", false, "do not fetch first")
	worktree.Flags().BoolVar(&options.Reuse, "reuse", false, "continue an existing branch or worktree")
	worktree.MarkFlagRequired("base")
	worktree.MarkFlagRequired("agent")
	cmd.AddCommand(clone, worktree, fetch, info, forget)
	return cmd
}
