// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newConfigCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Validate egzo.yaml and print the resolved configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			printWarnings(p.Warnings)

			encoder := yaml.NewEncoder(os.Stdout)
			encoder.SetIndent(2)
			defer encoder.Close()
			return encoder.Encode(p.Resolved)
		},
	}
}
