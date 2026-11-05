// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/access"
	"github.com/egzo-ai/egzo/internal/agentclient"
)

func newAgentAccessCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Say what this agent may reach (run inside an agent)",
		Long: "Inside an agent's container: its egress profile, the hosts it allows, each service with how to use its credential (the\n" +
			"proxy holds the real ones), and the ssh servers. Every other host is refused. It is what the `access` tool answers, for a\n" +
			"harness without MCP.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := agentclient.FromEnv()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			data, err := api.Do(ctx, "GET", "/v1/access", nil)
			if err != nil {
				return err
			}
			if asJSON {
				_, err := cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			var known access.Access
			if err := json.Unmarshal(data, &known); err != nil {
				return fmt.Errorf("the control sidecar's answer is unreadable: %w", err)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), known.Text())
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the answer as JSON, as the tool gives it")
	return cmd
}
