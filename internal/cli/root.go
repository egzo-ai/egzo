// Package cli implements the egzo command line.
package cli

import (
	"github.com/spf13/cobra"
)

// options are the global flags shared by every command.
type options struct {
	projectName string
	file        string
}

// New builds the root command.
func New() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "egzo",
		Short:         "Docker Compose for AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Like Docker Compose, -f comes before the command (`egzo -f x.yaml up`): after it, -f means --follow.
	root.TraverseChildren = true
	root.Flags().StringVarP(&opts.file, "file", "f", "", "the project file (default: egzo.yaml in this directory or the nearest parent; or EGZO_FILE)")
	root.PersistentFlags().StringVarP(&opts.projectName, "project-name", "p", "", "project name (default: name in egzo.yaml, else the directory name)")

	root.AddCommand(
		newInitCommand(),
		newConfigCommand(opts),
		newUpCommand(opts),
		newDownCommand(opts),
		newPsCommand(opts),
		newSendCommand(opts),
		newEventsCommand(opts),
		newQuestionsCommand(opts),
		newAnswerCommand(opts),
		newLogsCommand(opts),
		newExecCommand(opts),
		newLifecycleCommand(opts, "start", "Start a service's container"),
		newLifecycleCommand(opts, "stop", "Stop a service's container"),
		newLifecycleCommand(opts, "restart", "Restart a service's container"),
		newControlCommand(),
		newAgentCommand(),
		newPrepCommand(),
		newHookCommand(),
		newAttachCommand(opts),
		newVersionCommand(),
		newSecretsCommand(opts),
		newDoctorCommand(opts),
		newDiffCommand(opts),
		newCACommand(opts),
		newProxyCommand(opts),
	)
	return root
}
