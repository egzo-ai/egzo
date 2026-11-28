// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/claudeahp"
	"github.com/egzo-ai/egzo/internal/harness"
	sess "github.com/egzo-ai/egzo/internal/session"
	"github.com/egzo-ai/egzo/internal/version"
)

// newClaudeAHPCommand is Claude Code's AHP adapter, inside an agent's container in AHP mode: `serve` is the AHP server egzo starts next to
// the TUI (harness.Plan.Background), `hook` what Claude Code's hooks run to reach it.
func newClaudeAHPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "claude-ahp",
		Short:  "Claude Code's AHP adapter (inside an agent's container in AHP mode)",
		Hidden: true,
	}
	var transcript string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the chat of Claude Code's main session over AHP, on the address egzo described in ~/.egzo/ahp.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := harness.ReadAHPEndpoint(agentHome())
			if err != nil {
				return err
			}
			data, err := os.ReadFile(endpoint.MainSession)
			if err != nil {
				return fmt.Errorf("the main session is not named: %w", err)
			}
			u, err := url.Parse(endpoint.URL)
			if err != nil {
				return err
			}
			integration, _ := harness.For("claude-code")
			return claudeahp.Run(cmd.Context(), claudeahp.Options{
				Session:      strings.TrimSpace(string(data)),
				Transcript:   transcript,
				Listen:       u.Host,
				Path:         u.Path,
				HookPath:     claudeahp.HookPath,
				Token:        endpoint.Token,
				Socket:       sess.SocketPath(),
				ReadyMarkers: integration.ReadyMarkers(),
				InterruptKey: "\x1b",
				Version:      version.Version,
			})
		},
	}
	serve.Flags().StringVar(&transcript, "transcript", "", "the main session's transcript")
	hook := &cobra.Command{
		Use:   "hook",
		Short: "Pass one of Claude Code's hook events (on stdin) to the adapter, and print its answer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// never in Claude Code's way: an adapter that is not there, or fails, is reported in Claude Code's debug log, nothing more
			endpoint, err := harness.ReadAHPEndpoint(agentHome())
			if err != nil {
				fmt.Fprintln(os.Stderr, "egzo:", err)
				return nil
			}
			if err := claudeahp.SendHook(cmd.Context(), endpoint.URL, endpoint.Token, os.Stdin, os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "egzo:", err)
			}
			return nil
		},
	}
	cmd.AddCommand(serve, hook)
	return cmd
}

func agentHome() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	return "/home/agent"
}
