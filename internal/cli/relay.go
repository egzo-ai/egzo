// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/relay"
)

// newRelayCommand is the relay role: it carries connections from the ports published on the machine to the ports of one agent. It runs
// inside the egzo image, one container for each instance that declares ports.
func newRelayCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "relay",
		Short:  "Run the relay of an instance (runs inside a container)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(cmd *cobra.Command, args []string) error { return relay.Run() },
	}
}
