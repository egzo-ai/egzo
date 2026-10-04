package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/version"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the egzo version",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, args []string) { fmt.Fprintf(cmd.OutOrStdout(), "egzo %s\n", version.Version) },
	}
}
