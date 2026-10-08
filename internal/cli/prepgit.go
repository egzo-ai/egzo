// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/gitprep"
)

func newPrepGitCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "git", Short: "Git jobs for git workspaces", Hidden: true}
	var url, branch, base, agent string

	clone := &cobra.Command{
		Use:   "clone DIR",
		Short: "Clone a repository into DIR",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return gitprep.Clone(url, branch, args[0]) },
	}
	worktree := &cobra.Command{
		Use:   "worktree DIR",
		Short: "Make DIR a worktree of the base clone, cloning the base first when needed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return gitprep.Worktree(url, branch, base, args[0], agent)
		},
	}
	status := &cobra.Command{
		Use:   "status DIR...",
		Short: "Report the work in each checkout that exists nowhere else",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, dir := range args {
				findings, err := gitprep.Inspect(dir)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s uncommitted=%d unpushed=%d stashes=%d ignored=%d\n", dir, findings.Uncommitted, findings.Unpushed, findings.Stashes, findings.Ignored)
			}
			return nil
		},
	}
	for _, c := range []*cobra.Command{clone, worktree} {
		c.Flags().StringVar(&url, "url", "", "the repository (https)")
		c.Flags().StringVar(&branch, "branch", "", "the branch to check out")
		c.MarkFlagRequired("url")
	}
	worktree.Flags().StringVar(&base, "base", "", "the base clone the worktree belongs to")
	worktree.Flags().StringVar(&agent, "agent", "", "the agent the worktree is for")
	worktree.MarkFlagRequired("base")
	worktree.MarkFlagRequired("agent")
	cmd.AddCommand(clone, worktree, status)
	return cmd
}
