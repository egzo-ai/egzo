package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/control"
)

// newControlCommand is the control sidecar role. It runs inside the egzo image, not on the user's machine.
func newControlCommand() *cobra.Command {
	var healthcheck bool
	cmd := &cobra.Command{
		Use:    "control",
		Short:  "Run the control sidecar (runs inside a container)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if healthcheck {
				return control.Healthcheck()
			}
			return control.Run()
		},
	}
	cmd.Flags().BoolVar(&healthcheck, "healthcheck", false, "exit 0 when the sidecar is healthy")
	cmd.AddCommand(&cobra.Command{
		Use:   "request METHOD PATH",
		Short: "Send one request to the operator API, with stdin as the body",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := control.Request(args[0], args[1], cmd.InOrStdin())
			os.Stdout.Write(body)
			return err
		},
	})
	return cmd
}
