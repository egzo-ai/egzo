// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/operator"
	"github.com/egzo-ai/egzo/internal/proxy"
)

func newProxyCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "The egress proxy and its audit trail",
	}
	cmd.AddCommand(
		newProxyLogCommand(opts),
		newProxyRulesCommand(opts),
		&cobra.Command{
			Use:    "serve",
			Short:  "Run the egress proxy (runs inside a container)",
			Hidden: true,
			Args:   cobra.NoArgs,
			RunE:   func(cmd *cobra.Command, args []string) error { return proxy.Run(proxy.DefaultConfig(), os.Stdout) },
		},
		&cobra.Command{
			Use:    "healthcheck",
			Short:  "Exit 0 when the proxy is healthy (runs inside a container)",
			Hidden: true,
			Args:   cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return operator.Healthcheck(proxy.DefaultConfig().Socket)
			},
		},
		&cobra.Command{
			Use:    "request METHOD PATH",
			Short:  "Send one request to the proxy's operator API (runs inside a container)",
			Hidden: true,
			Args:   cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				body, err := operator.Request(proxy.DefaultConfig().Socket, args[0], args[1], cmd.InOrStdin())
				fmt.Fprint(os.Stdout, string(body))
				return err
			},
		},
	)
	return cmd
}
