package cli

import "github.com/spf13/cobra"

func newPrepGitCommand() *cobra.Command { return &cobra.Command{Use: "git", Hidden: true} }
