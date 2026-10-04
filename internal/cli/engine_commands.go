package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
	"github.com/egzo-ai/egzo/internal/version"
)

// EnvImage overrides the all-in-one egzo image that runs the sidecars.
const EnvImage = "EGZO_IMAGE"

// EnvHarnessPrefix overrides where harness images come from: the image of a harness is
// <prefix><harness>:<version>, by default ghcr.io/egzo-ai/egzo-harness-<harness>:<version>.
const EnvHarnessPrefix = "EGZO_HARNESS_PREFIX"

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
	c, err := engine.Connect(ctx)
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
				stack.Options{DryRun: dryRun, Recreate: recreate, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix)}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate containers even when their configuration is unchanged")
	return cmd
}

func newDownCommand(opts *options) *cobra.Command {
	var volumes, workspaces, yes, force bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the project's containers and networks",
		Long: "Remove the project's containers and networks. Volumes are kept unless --volumes is given.\n" +
			"Workspace directories on the host are never deleted, because they hold work that exists nowhere\n" +
			"else. --workspaces removes the git checkouts, after looking in each for uncommitted files and\n" +
			"unpushed commits and refusing when it finds any (--force removes them anyway), and after asking.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			var doomed []string
			if workspaces {
				doomed, err = chooseWorkspacesToRemove(ctx, cmd, s, yes, force)
				if err != nil {
					return err
				}
			}
			if err := stack.Down(ctx, s.engine, s.observed, volumes, cmd.OutOrStdout()); err != nil {
				return err
			}
			for _, dir := range doomed {
				fmt.Fprintf(cmd.OutOrStdout(), "remove workspace %s\n", dir)
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
			}
			for _, root := range stack.WorkspaceRoots(s.Resolved) {
				os.Remove(root) // only when nothing is left in it
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also remove the project's volumes")
	cmd.Flags().BoolVar(&workspaces, "workspaces", false, "also remove the git checkouts on the host, once they are known to hold no unsaved work")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask before removing workspaces")
	cmd.Flags().BoolVar(&force, "force", false, "remove workspaces even when they hold uncommitted or unpushed work")
	return cmd
}

// chooseWorkspacesToRemove returns the checkouts down --workspaces may remove, or an error when one
// holds unsaved work or the user says no. It runs before anything is removed.
func chooseWorkspacesToRemove(ctx context.Context, cmd *cobra.Command, s *session, yes, force bool) ([]string, error) {
	user := stack.AgentUser(s.engine)
	unsaved, err := stack.InspectCheckouts(ctx, s.engine, s.Resolved, s.Dir, imageRef(), user)
	if err != nil {
		return nil, err
	}
	if len(unsaved) > 0 && !force {
		lines := make([]string, len(unsaved))
		for i, u := range unsaved {
			lines[i] = "  " + u.String()
		}
		return nil, fmt.Errorf("refusing to remove workspaces that hold unsaved work (uncommitted files or unpushed commits):\n%s\n"+
			"commit and push it, or use --force to throw it away", strings.Join(lines, "\n"))
	}
	var existing []string
	for _, dir := range stack.GitDirs(s.Resolved) {
		if _, err := os.Stat(dir); err == nil {
			existing = append(existing, dir)
		}
	}
	if len(existing) == 0 || yes {
		return existing, nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Remove these workspace directories?\n  %s\n[y/N] ", strings.Join(existing, "\n  "))
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return nil, fmt.Errorf("aborted: nothing was removed")
	}
	return existing, nil
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
			stack.WriteStatus(s.observed, reportedStatuses(ctx, s), cmd.OutOrStdout())
			return nil
		},
	}
}

// reportedStatuses asks the control sidecar what each agent last said about itself. It is best
// effort: ps still works when the sidecar is not running.
func reportedStatuses(ctx context.Context, s *session) map[string]stack.Report {
	reported := map[string]stack.Report{}
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/agents", nil)
	if err != nil {
		return reported
	}
	var statuses []struct{ Agent, Status, Activity string }
	if json.Unmarshal(reply, &statuses) == nil {
		for _, status := range statuses {
			reported[status.Agent] = stack.Report{Status: status.Status, Activity: status.Activity}
		}
	}
	return reported
}
