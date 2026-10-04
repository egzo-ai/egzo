package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
				err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					return os.Lchown(path, uid, gid)
				})
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
