// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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

// prepEnv is what the prep containers of this session need: the egzo image and the user who owns the volumes. A
// published project says both (they are what `up` used, whoever runs this command); otherwise this process's own.
func (s *session) prepEnv(ctx context.Context) stack.PrepEnv {
	if published, err := stack.ReadPublished(ctx, s.engine, s.Resolved.Name); err == nil && published.Image != "" {
		return stack.PrepEnv{Image: published.Image, User: published.User}
	}
	return stack.PrepEnv{Image: imageRef(), User: stack.AgentUser(s.engine)}
}

func openSession(ctx context.Context, opts *options) (*session, error) {
	p, err := loadProject(opts)
	if err != nil {
		return nil, err
	}
	if !opts.quiet {
		printWarnings(p.Warnings)
	}
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
			"with `egzo agent spawn TEMPLATE`. up always returns once converged; there is no foreground mode.\n" +
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
				release, err := stack.Lock(ctx, s.engine, s.Resolved.Name, imageRef())
				if err != nil {
					return err
				}
				defer release()
			}

			options := stack.Options{DryRun: dryRun, Recreate: recreate, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix), Warnings: cmd.ErrOrStderr()}
			if !dryRun {
				return stack.Up(ctx, s.engine, s.Resolved, s.Dir, options, cmd.OutOrStdout())
			}
			changes, err := dryRunUp(ctx, s, options, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if changes > 0 {
				return ExitError{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "recreate the sidecars even when their configuration is unchanged")
	return cmd
}

// dryRunUp prints what `up` would do and counts the changes: what a dry run says about checking, not about
// changing, is not one.
func dryRunUp(ctx context.Context, s *session, options stack.Options, out io.Writer) (int, error) {
	var buffer bytes.Buffer
	if err := stack.Up(ctx, s.engine, s.Resolved, s.Dir, options, &buffer); err != nil {
		return 0, err
	}
	changes := 0
	scanner := bufio.NewScanner(&buffer)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(out, line)
		if line != "nothing to do" && !strings.HasPrefix(line, "would check") {
			changes++
		}
	}
	return changes, nil
}

func newDownCommand(opts *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the project's containers, networks and volumes",
		Long: "Remove everything egzo made for the project: the sidecars, the networks, the volumes (the control state and the\n" +
			"CA, and the home of every agent) and the worktrees of the agents it removes. It is refused while an agent\n" +
			"runs, and removes nothing then; --force removes the agents too, as `egzo agent rm --force` does. The\n" +
			"volumes of the directory workspaces and of the base clones are kept: they are your work, and a later\n" +
			"`egzo project up` finds them again. `egzo workspace rm` deletes them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			release, err := stack.Lock(ctx, s.engine, s.Resolved.Name, imageRef())
			if err != nil {
				return err
			}
			defer release()
			// what an instance made while this command waited for the lock must be removed too
			if s.observed, err = stack.Observe(ctx, s.engine, s.Resolved.Name); err != nil {
				return err
			}
			var running []string
			for _, instance := range s.observed.Instances() {
				if instance.State == "running" {
					running = append(running, instance.Name)
				}
			}
			if len(running) > 0 && !force {
				return fmt.Errorf("agents are running: %s; stop them (egzo stop NAME) or use --force to remove them, with their worktrees and homes",
					strings.Join(running, ", "))
			}
			return stack.Down(ctx, s.engine, s.observed, s.prepEnv(ctx), s.Resolved.Name, s.Dir, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove running agents too")
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
			reported := reportedStatuses(ctx, s)
			reportLink(ctx, s, reported)
			rows := stack.Rows(s.observed, reported, published)
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
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/v1/agents", nil)
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

// reportLink adds where the link stands to what ps reports. Best effort, like the sidecar's statuses.
func reportLink(ctx context.Context, s *session, reported map[string]stack.Report) {
	if s.Resolved.Hub == nil {
		return
	}
	if status, err := stack.LinkStatus(ctx, s.engine, s.Resolved.Name, 0); err == nil {
		reported[stack.LinkReport] = stack.Report{Status: status.Text()}
	}
}
