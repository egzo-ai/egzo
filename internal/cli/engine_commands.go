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
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
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
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	// The first Ctrl-C asks the command to stop; once it has, the default behaviour comes back, so a
	// second one ends a command that is slow to wind down.
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

func newUpCommand(opts *options) *cobra.Command {
	var dryRun, recreate bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Bring up the project's infrastructure and publish its templates",
		Long: "Bring up the project's infrastructure (the control sidecar, the egress proxy, their networks and volumes),\n" +
			"load the egress policy and publish the agent templates in egzo.yaml. up starts no agent: spawn one\n" +
			"with `egzo spawn TEMPLATE`. up always returns once converged; there is no foreground mode.\n" +
			"It refuses while an instance is stale (its template changed or is gone): remove those first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			if !dryRun {
				release, err := stack.Lock(s.Dir)
				if err != nil {
					return err
				}
				defer release()
			}

			return stack.Up(ctx, s.engine, s.Resolved, s.Dir,
				stack.Options{DryRun: dryRun, Recreate: recreate, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix)}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate the sidecars even when their configuration is unchanged")
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
			release, err := stack.Lock(s.Dir)
			if err != nil {
				return err
			}
			defer release()
			var doomed []string
			if workspaces {
				// Nothing may write to a checkout while it is inspected and while the person decides:
				// the agents are stopped first, and started again if the removal is refused.
				stopped, err := stack.StopAgents(ctx, s.engine, s.observed)
				if err != nil {
					return err
				}
				doomed, err = chooseWorkspacesToRemove(ctx, cmd, s, yes, force)
				if err != nil {
					stack.StartAgents(ctx, s.engine, stopped)
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

// removableCheckouts splits the directories down --workspaces could remove into real checkouts, which may
// go, and everything else, which is left alone with the reason.
func removableCheckouts(project *config.Resolved, projectDir string) (ok, skipped []string) {
	for _, dir := range stack.GitDirs(project) {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := stack.RemovalProblem(dir, projectDir); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		ok = append(ok, dir)
	}
	return ok, skipped
}

// chooseWorkspacesToRemove returns the checkouts down --workspaces may remove, or an error when one
// holds unsaved work or the user says no. It runs before anything is removed.
func chooseWorkspacesToRemove(ctx context.Context, cmd *cobra.Command, s *session, yes, force bool) ([]string, error) {
	user := stack.AgentUser(s.engine)
	unsaved, err := stack.InspectCheckouts(ctx, s.engine, s.Resolved, s.Dir, imageRef(), user)
	if err != nil {
		return nil, err
	}
	var blocking []stack.Unsaved
	ignored := map[string]int{}
	for _, u := range unsaved {
		if u.Blocks() {
			blocking = append(blocking, u)
		}
		ignored[u.Dir] = u.Ignored
	}
	if len(blocking) > 0 && !force {
		lines := make([]string, len(blocking))
		for i, u := range blocking {
			lines[i] = "  " + u.String()
		}
		return nil, fmt.Errorf("refusing to remove workspaces that hold unsaved work (uncommitted files, unpushed commits or stashes):\n%s\n"+
			"commit and push it, or use --force to throw it away", strings.Join(lines, "\n"))
	}
	existing, skipped := removableCheckouts(s.Resolved, s.Dir)
	for _, reason := range skipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "not removing: %s\n", reason)
	}
	if len(existing) == 0 || yes {
		return existing, nil
	}
	var listing []string
	for _, dir := range existing {
		line := dir
		if n := ignored[dir]; n > 0 {
			line += fmt.Sprintf("  (%d ignored file(s) or director(ies) in no commit will be deleted too)", n)
		}
		listing = append(listing, line)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Remove these workspace directories?\n  %s\n[y/N] ", strings.Join(listing, "\n  "))
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return nil, fmt.Errorf("aborted: nothing was removed")
	}
	return existing, nil
}

func newPsCommand(opts *options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List the project's containers: the sidecars and the instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			// Stale is judged against what `up` would publish from the file: up refuses before it publishes.
			var published *stack.Published
			if p, err := currentTemplates(s); err == nil {
				published = &p
			}
			rows := stack.Rows(s.observed, reportedStatuses(ctx, s), published)
			if asJSON {
				if rows == nil {
					rows = []stack.Row{}
				}
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(rows)
			}
			stack.WriteStatus(rows, time.Now(), cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the containers as a JSON array")
	return cmd
}

// reportedStatuses asks the control sidecar what each agent last said about itself. It is best
// effort: ps still works when the sidecar is not running.
func reportedStatuses(ctx context.Context, s *session) map[string]stack.Report {
	reported := map[string]stack.Report{}
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/agents", nil)
	if err != nil {
		return reported
	}
	var statuses []struct {
		Agent, Status, Activity string
		Open                    int
		Waiting                 bool
	}
	if json.Unmarshal(reply, &statuses) == nil {
		for _, status := range statuses {
			reported[status.Agent] = stack.Report{Status: status.Status, Activity: status.Activity, Open: status.Open, Waiting: status.Waiting}
		}
	}
	return reported
}
