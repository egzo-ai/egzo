package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
	"github.com/egzo-ai/egzo/internal/version"
)

// EnvImage overrides the all-in-one egzo image that runs the sidecars.
const EnvImage = "EGZO_IMAGE"

func imageRef() string {
	if image := os.Getenv(EnvImage); image != "" {
		return image
	}
	return "ghcr.io/egzo-ai/egzo:" + version.Version
}

// session is a loaded project connected to its engine, with its resources observed. Every
// command that touches the engine starts here, so the same-name-other-directory refusal applies
// to all of them.
type session struct {
	*project
	engine   *engine.Client
	observed stack.Observed
}

func (s *session) close() { s.engine.Close() }

func openSession(ctx context.Context, opts *options) (*session, error) {
	p, err := loadProject(opts)
	if err != nil {
		return nil, err
	}
	printWarnings(p.Warnings)
	c, err := engine.Connect(ctx, p.Resolved.Runtime.Engine)
	if err != nil {
		return nil, err
	}
	observed, err := stack.Observe(ctx, c, p.Resolved.Name)
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := stack.CheckOwnership(observed, p.Resolved.Name, p.Dir); err != nil {
		c.Close()
		return nil, err
	}
	return &session{project: p, engine: c, observed: observed}, nil
}

func commandContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
}

func newUpCommand(opts *options) *cobra.Command {
	var dryRun, recreate bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Converge the running project to egzo.yaml",
		Long: "Converge the running project to egzo.yaml. up always returns once the project is converged:\n" +
			"there is no foreground mode, agents are reached with `egzo attach`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()

			return stack.Up(ctx, s.engine, s.Resolved, s.Dir,
				stack.Options{DryRun: dryRun, Recreate: recreate, Image: imageRef()}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate containers even when their configuration is unchanged")
	return cmd
}

func newDownCommand(opts *options) *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the project's containers and networks",
		Long: "Remove the project's containers and networks. Volumes are kept unless --volumes is given.\n" +
			"Workspace directories on the host are never deleted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			return stack.Down(ctx, s.engine, s.observed, volumes, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also remove the project's volumes")
	return cmd
}

func newPsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List the project's containers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			stack.WriteStatus(s.observed, cmd.OutOrStdout())
			return nil
		},
	}
}
