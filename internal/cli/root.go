// Package cli implements the egzo command line.
package cli

import (
	"github.com/spf13/cobra"
)

// options are the global flags shared by every command.
type options struct {
	projectName string
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
	root.PersistentFlags().StringVarP(&opts.projectName, "project-name", "p", "", "project name (default: name in egzo.yaml, else the directory name)")

	root.AddCommand(
		newConfigCommand(opts),
		newUpCommand(opts),
		newDownCommand(opts),
		newPsCommand(opts),
	)
	return root
}
