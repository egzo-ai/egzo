// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package cli implements the egzo command line.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// options are the global flags shared by every command.
type options struct {
	projectName string
	file        string
	envWarned   bool // the env backend warning has been printed by this command
	quiet       bool // the project's warnings have been printed by this command: a session opened later does not repeat them
}

// New builds the root command.
func New() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "egzo",
		Short:         "Docker Compose for AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Args = cobra.ArbitraryArgs
	root.RunE = groupRun
	// Like Docker Compose, -f comes before the command (`egzo -f x.yaml up`): after it, -f means --follow.
	root.TraverseChildren = true
	root.Flags().StringVarP(&opts.file, "file", "f", "", "the project file (default: egzo.yaml in this directory or the nearest parent; or EGZO_FILE)")
	root.PersistentFlags().StringVarP(&opts.projectName, "project-name", "p", "", "project name (default: name in egzo.yaml, else the directory name)")

	root.AddCommand(
		newProjectCommand(opts),
		newAgentCommand(opts),
		newMessageCommand(opts),
		newWorkspaceCommand(opts),
		newSystemCommand(),
		newPsCommand(opts),
		newLogsCommand(opts),
		newExecCommand(opts),
		newLifecycleCommand(opts, "start", "Start a service's container"),
		newLifecycleCommand(opts, "stop", "Stop a service's container"),
		newLifecycleCommand(opts, "restart", "Restart a service's container"),
		newEventsCommand(opts),
		newProxyCommand(opts),
		newVersionCommand(),
		newControlCommand(),
		newPrepCommand(),
		newHookCommand(),
	)
	return root
}

// groupRun is what a command that only groups other commands does: show its help, or refuse a name it does not have.
func groupRun(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}

// group makes cmd a command that only groups others.
func group(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = groupRun
	return cmd
}
