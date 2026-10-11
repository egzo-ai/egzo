// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
				stack.Options{DryRun: dryRun, Recreate: recreate, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix), Warnings: cmd.ErrOrStderr()}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate the sidecars even when their configuration is unchanged")
	return cmd
}

func newDownCommand(opts *options) *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the project's containers and networks",
		Long: "Remove the project's containers and networks, and the worktrees of the instances it removes. Volumes are\n" +
			"kept unless --volumes is given. The base clones and the directory workspaces on the host are never\n" +
			"deleted: they are yours.",
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
			// what an instance made while this command waited for the lock must be removed too
			if s.observed, err = stack.Observe(ctx, s.engine, s.Resolved.Name); err != nil {
				return err
			}
			instances := s.observed.InstanceNames()
			if err := stack.Down(ctx, s.engine, s.observed, volumes, cmd.OutOrStdout()); err != nil {
				// the instances that did go take their worktrees with them; the ones that are still there keep theirs
				if again, observeErr := stack.Observe(ctx, s.engine, s.Resolved.Name); observeErr == nil {
					still := map[string]bool{}
					for _, name := range again.InstanceNames() {
						still[name] = true
					}
					var gone []string
					for _, name := range instances {
						if !still[name] {
							gone = append(gone, name)
						}
					}
					stack.RemoveWorktrees(s.Resolved, gone, cmd.OutOrStdout())
				}
				return err
			}
			return stack.RemoveWorktrees(s.Resolved, instances, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also remove the project's volumes")
	return cmd
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
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: cannot tell which instances are stale: %v\n", err)
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
