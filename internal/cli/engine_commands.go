package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

var errNotImplemented = errors.New("not implemented yet")

func newUpCommand(opts *options) *cobra.Command {
	var dryRun, recreate bool
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Converge the running project to egzo.yaml",
		Long: "Converge the running project to egzo.yaml. up always returns once the project is converged:\n" +
			"there is no foreground mode, agents are reached with `egzo attach`.",
		RunE: func(cmd *cobra.Command, args []string) error { return errNotImplemented },
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate containers even when their configuration is unchanged")
	return cmd
}

func newDownCommand(opts *options) *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the project's containers and networks",
		RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also remove the project's volumes")
	return cmd
}

func newPsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List the project's containers and agent status",
		RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
	}
}
