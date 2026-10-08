// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// newPrepCommand is the prep role: short-lived containers that do one job for the CLI. They run
// inside the egzo image, never on the user's machine.
func newPrepCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "prep",
		Short:  "Jobs the CLI runs in short-lived containers",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "chown UID:GID PATH...",
		Short: "Give directory trees to a user",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, gid, err := parseOwner(args[0])
			if err != nil {
				return err
			}
			for _, root := range args[1:] {
				err := chownTree(root, uid, gid, os.Lchown)
				if err != nil {
					return err
				}
			}
			return nil
		},
	})
	cmd.AddCommand(newPrepGitCommand())
	return cmd
}

func parseOwner(spec string) (int, int, error) {
	user, group, ok := strings.Cut(spec, ":")
	if !ok {
		return 0, 0, fmt.Errorf("owner %q must be UID:GID", spec)
	}
	uid, err := strconv.Atoi(user)
	if err != nil {
		return 0, 0, fmt.Errorf("owner %q must be UID:GID", spec)
	}
	gid, err := strconv.Atoi(group)
	if err != nil {
		return 0, 0, fmt.Errorf("owner %q must be UID:GID", spec)
	}
	return uid, gid, nil
}

// chownTree gives a directory tree to uid:gid, touching only what is not theirs already: a recreated
// agent finds its volumes (a workspace with a whole node_modules in it) owned correctly, and walking
// them to say so again would cost seconds for nothing. Symbolic links are changed, never followed.
func chownTree(root string, uid, gid int, chown func(path string, uid, gid int) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, err := entry.Info(); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) == uid && int(stat.Gid) == gid {
				return nil
			}
		}
		return chown(path, uid, gid)
	})
}
