// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/files"
)

// newFilesCommand is the role of a workspace's file helper: the container the project's link makes to show the hub a workspace's files,
// read only. It runs inside the egzo image, never on the user's machine.
func newFilesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "files",
		Short:  "Serve a workspace's files to the hub, read only (runs inside a container)",
		Hidden: true,
	}
	var root, socket string
	var idle time.Duration
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the files under --root on a unix socket until idle",
		Long: "Serve the files under --root, read only, on the unix socket --socket: GET of a directory (its path ends with a slash)\n" +
			"lists it as JSON, GET of a regular file sends its bytes. Nothing outside the root is reached, symbolic links are\n" +
			"not followed, and there is no way to write. It exits after --idle with no connection.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			return files.Serve(ctx, root, socket, files.ServeOptions{Idle: idle})
		},
	}
	serve.Flags().StringVar(&root, "root", "/w", "the directory to serve")
	serve.Flags().StringVar(&socket, "socket", files.Socket, "where to listen")
	serve.Flags().DurationVar(&idle, "idle", 5*time.Minute, "exit after this long with no connection")
	var announce bool
	connect := &cobra.Command{
		Use:   "connect",
		Short: "Carry one connection to the file server over stdin and stdout",
		Long: "Connect to the file server's socket and copy bytes between it and stdin and stdout, until either ends. What the\n" +
			"project's link runs in the helper for each connection. --announce says on stderr when it reached the server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var connected func()
			if announce {
				connected = func() { fmt.Fprintln(os.Stderr, files.Connected) }
			}
			if err := files.Connect(socket, os.Stdin, os.Stdout, connected); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return ExitError{Code: 1}
			}
			return nil
		},
	}
	connect.Flags().StringVar(&socket, "socket", files.Socket, "the server's socket")
	connect.Flags().BoolVar(&announce, "announce", false, "say on stderr when the server has been reached")
	cmd.AddCommand(serve, connect)
	return group(cmd)
}
