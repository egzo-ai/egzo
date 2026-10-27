// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

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
		newProxyLogoutCommand(opts),
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

func newProxyLogoutCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout SERVICE",
		Short: "End the sign-in of an OAuth service: the proxy revokes its token and forgets it",
		Long: "The proxy keeps the sign-in of each OAuth service of the project (such as chatgpt), shared by every agent.\n" +
			"logout revokes it at the provider when the service has a revoke URL, and the agents get \"sign in again\" until one\n" +
			"of them signs in. `egzo project down` ends the sign-ins too.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			if _, err := proxyRequest(ctx, s, "POST", "/oauth/"+args[0]+"/logout", nil); err != nil {
				return fmt.Errorf("logout %s: %w", args[0], err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "signed out of %s\n", args[0])
			return nil
		},
	}
}

// proxyRequest sends a request to the running proxy's operator API.
func proxyRequest(ctx context.Context, s *session, method, path string, body []byte) ([]byte, error) {
	sidecar, err := s.service("proxy")
	if err != nil {
		return nil, err
	}
	if sidecar.State != "running" {
		return nil, fmt.Errorf("the proxy is %s: run `egzo project up` first", sidecar.State)
	}
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(body)
	}
	result, err := s.engine.Exec(ctx, sidecar.ID, []string{"/egzo", "proxy", "request", method, path}, input)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(result.Stderr)))
	}
	return result.Stdout, nil
}

// signInStatuses asks the running proxy about the sign-ins of the project's OAuth services.
func signInStatuses(ctx context.Context, s *session) (map[string]proxy.SignInStatus, error) {
	body, err := proxyRequest(ctx, s, "GET", "/oauth", nil)
	if err != nil {
		return nil, err
	}
	statuses := map[string]proxy.SignInStatus{}
	return statuses, json.Unmarshal(body, &statuses)
}
