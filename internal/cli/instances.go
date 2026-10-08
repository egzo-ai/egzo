// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

// lockWait is how long spawn and rm wait for another command that is changing the project: spawning
// several instances at once is what they are for, and a clone can take a while.
const lockWait = 10 * time.Minute

// exitExists is the exit code of `egzo spawn` when the name is taken (EEXIST).
const exitExists = 17

// currentTemplates is what `up` would publish from the file as it is now. Staleness is judged against
// it: `up` refuses before it publishes, so the last published templates never differ from the instances.
func currentTemplates(s *session) (stack.Published, error) {
	return stack.Publish(s.Resolved, s.Dir, imageRef(), os.Getenv(EnvHarnessPrefix), stack.AgentUser(s.engine))
}

func newSpawnCommand(opts *options) *cobra.Command {
	var message string
	var attach, wait, asJSON bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "spawn TEMPLATE [NAME]",
		Short: "Start an agent from a template",
		Long: "Start an agent from one of the templates `egzo up` published (the agents of egzo.yaml). NAME is the\n" +
			"instance's name, `<template>-<n>` when left out. -m sends it a first message, the task. A name that\n" +
			"is taken fails with exit code 17 and changes nothing.\n" +
			"The instance's name is printed to stdout. --attach attaches to it; --wait (with -m) waits for the\n" +
			"message to be resolved, prints the resolution to stdout and exits as `send --wait` does.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case asJSON && (wait || attach):
				return fmt.Errorf("--json cannot be used with --wait or --attach: they print and take over the terminal themselves")
			case wait && message == "":
				return fmt.Errorf("--wait needs -m: it waits for the first message to be resolved")
			case wait && attach:
				return fmt.Errorf("--wait and --attach cannot be used together")
			case timeout != 0 && !wait:
				return fmt.Errorf("--timeout only applies to --wait")
			case attach && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))):
				return fmt.Errorf("attach needs a terminal on both stdin and stdout")
			case message != "" && strings.TrimSpace(message) == "":
				return fmt.Errorf("-m needs a message")
			}
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()

			request := stack.SpawnRequest{Template: args[0], Actor: operatorActor}
			if len(args) == 2 {
				request.Name = args[1]
			}
			spawned, err := spawnLocked(ctx, s, request, cmd)
			var exists *stack.ExistsError
			if errors.As(err, &exists) {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return ExitError{Code: exitExists}
			}
			if err != nil {
				return err
			}
			warnIfTemplateMoved(cmd, s, spawned)
			// the new instance is a fact now: messages and attach find it
			if observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name); err == nil {
				s.observed = observed
			}

			switch {
			case asJSON && !wait:
				data, _ := json.Marshal(map[string]string{"name": spawned.Name, "service": spawned.Template, "container": spawned.Container, "actor": spawned.Actor})
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			case wait:
				fmt.Fprintln(cmd.ErrOrStderr(), spawned.Name)
			default:
				fmt.Fprintln(cmd.OutOrStdout(), spawned.Name)
			}
			if message == "" {
				if attach {
					return attachAfterSpawn(ctx, s, spawned.Name)
				}
				return nil
			}
			queued, err := queueMessage(ctx, s, spawned.Name, message, false)
			if err != nil {
				return fmt.Errorf("%s was spawned, but its first message could not be sent: %w", spawned.Name, err)
			}
			if wait {
				fmt.Fprintf(cmd.ErrOrStderr(), "queued %s for %s\n", queued.ID, spawned.Name)
				return waitForResolution(ctx, cmd, s, spawned.Name, queued.ID, queued.Seq, timeout)
			}
			if attach {
				return attachAfterSpawn(ctx, s, spawned.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "the first message: the instance's task")
	cmd.Flags().BoolVar(&attach, "attach", false, "attach to the instance once it is up")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the first message to be resolved and print the answer (needs -m)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "with --wait, give up after this long (exit code 5); the message stays open")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the instance as one JSON object")
	return cmd
}

// lockOrWait takes the project lock, saying so when it has to wait for another command.
func lockOrWait(s *session, cmd *cobra.Command) (func(), error) {
	if release, err := stack.Lock(s.Dir); err == nil {
		return release, nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "waiting for another egzo command that is changing this project...")
	return stack.LockWait(s.Dir, lockWait)
}

// warnIfTemplateMoved says so when egzo.yaml no longer matches the template an instance was made from:
// spawn uses what `up` published, and an instance of an outdated template is stale at once.
func warnIfTemplateMoved(cmd *cobra.Command, s *session, spawned stack.Spawned) {
	current, err := currentTemplates(s)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: cannot tell whether egzo.yaml still matches the published templates: %v\n", err)
		return
	}
	if template, ok := current.Templates[spawned.Template]; !ok || template.Hash != spawned.TemplateHash {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: egzo.yaml has changed since `egzo up`: %s was made from the published template, so it is stale already; "+
			"run `egzo rm %s`, then `egzo up`, then spawn it again\n", spawned.Name, spawned.Name)
	}
}

// spawnLocked spawns under the project lock, which is released before the caller waits or attaches.
func spawnLocked(ctx context.Context, s *session, request stack.SpawnRequest, cmd *cobra.Command) (stack.Spawned, error) {
	release, err := lockOrWait(s, cmd)
	if err != nil {
		return stack.Spawned{}, err
	}
	defer release()
	return stack.Spawn(ctx, s.engine, s.Resolved.Name, s.Dir, request, cmd.ErrOrStderr())
}

func attachAfterSpawn(ctx context.Context, s *session, name string) error {
	observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name)
	if err != nil {
		return err
	}
	s.observed = observed
	return attachTo(ctx, s, name, envOr("EGZO_DETACH_KEYS", "ctrl-]"), false)
}

// removalPlan is what rm and prune are about to do: the instances, and the checkouts that go with them.
type removalPlan struct {
	names []string
	dirs  []string // checkouts to remove, with --workspaces
}

// viewOfInstances is the project with the given instances as its agents, from the published templates
// when they can be read and from the file otherwise: it says which checkouts belong to each instance.
func viewOfInstances(ctx context.Context, cmd *cobra.Command, s *session, instances []stack.Instance) *config.Resolved {
	byName := map[string]string{}
	for _, instance := range instances {
		byName[instance.Name] = instance.Template
	}
	if published, err := stack.ReadPublished(ctx, s.engine, s.Resolved.Name); err == nil {
		return published.View(byName)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: the published templates cannot be read: the checkouts are found from egzo.yaml")
	view := *s.Resolved
	view.Agents = map[string]config.ResolvedAgent{}
	for name, template := range byName {
		if agent, ok := s.Resolved.Agents[template]; ok {
			view.Agents[name] = agent
		}
	}
	return &view
}

// removeInstances removes the instances named, and with workspaces the checkouts of their own, after the
// inspection `down --workspaces` does. Nothing is removed unless everything may be.
func removeInstances(ctx context.Context, cmd *cobra.Command, s *session, names []string, workspaces, force, yes bool) ([]string, error) {
	release, err := lockOrWait(s, cmd)
	if err != nil {
		return nil, err
	}
	defer release()
	observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name)
	if err != nil {
		return nil, err
	}
	return removeObserved(ctx, cmd, s, observed, names, workspaces, force, yes)
}

// removeObserved is removeInstances under the lock, for what was just observed.
func removeObserved(ctx context.Context, cmd *cobra.Command, s *session, observed stack.Observed, names []string, workspaces, force, yes bool) ([]string, error) {
	var err error
	var instances []stack.Instance
	for _, name := range names {
		instance, ok := observed.Instance(name)
		if !ok {
			if _, isTemplate := s.Resolved.Agents[name]; isTemplate {
				return nil, fmt.Errorf("%s is a template, not an instance: nothing to remove (instances: %s; `egzo spawn %s` makes one)", name, strings.Join(observed.InstanceNames(), ", "), name)
			}
			return nil, fmt.Errorf("no instance %q in this project (instances: %s)", name, strings.Join(observed.InstanceNames(), ", "))
		}
		if instance.State == "running" && !force {
			return nil, fmt.Errorf("instance %q is running: stop it first (egzo stop %s) or use --force", name, name)
		}
		instances = append(instances, instance)
	}

	var doomed []string
	if workspaces {
		view := viewOfInstances(ctx, cmd, s, instances)
		var dirs []string
		for _, instance := range instances {
			dirs = append(dirs, stack.InstanceGitDirs(view, instance.Name)...)
		}
		// Nothing may write to a checkout while it is inspected and while the person decides.
		var stopped []stack.Resource
		for _, instance := range instances {
			if instance.State != "running" {
				continue
			}
			for _, r := range observed.Resources {
				if r.Type == "container" && r.Instance == instance.Name && r.Kind == "agent" {
					timeout := 5
					if err := s.engine.API.ContainerStop(ctx, r.ID, container.StopOptions{Timeout: &timeout}); err != nil {
						stack.StartAgents(ctx, s.engine, stopped)
						return nil, fmt.Errorf("stop %s: %w", instance.Name, err)
					}
					stopped = append(stopped, r)
				}
			}
		}
		doomed, err = chooseCheckoutsToRemove(ctx, cmd, s, dirs, yes, force)
		if err != nil {
			stack.StartAgents(ctx, s.engine, stopped)
			return nil, err
		}
	}
	removed, err := stack.RemoveInstances(ctx, s.engine, observed, s.Resolved.Name, names, true, cmd.ErrOrStderr())
	if err != nil {
		return removed, err
	}
	for _, dir := range doomed {
		fmt.Fprintf(cmd.ErrOrStderr(), "remove workspace %s\n", dir)
		if err := os.RemoveAll(dir); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// chooseCheckoutsToRemove is chooseWorkspacesToRemove for a given list of directories.
func chooseCheckoutsToRemove(ctx context.Context, cmd *cobra.Command, s *session, dirs []string, yes, force bool) ([]string, error) {
	var existing []string
	var skipped []string
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := stack.RemovalProblem(dir, s.Dir); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		existing = append(existing, dir)
	}
	unsaved, err := stack.InspectDirs(ctx, s.engine, s.Resolved.Name, existing, s.Dir, imageRef(), stack.AgentUser(s.engine))
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

func newRmCommand(opts *options) *cobra.Command {
	var workspaces, force, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "rm INSTANCE...",
		Short: "Remove instances",
		Long: "Remove instances: the container, the network and the home volume, and the agent's registration in the\n" +
			"control sidecar and the proxy. What was asked of the agent and is still open is closed as failed.\n" +
			"A running instance needs --force. Checkouts on the host are kept because they hold work: --workspaces\n" +
			"removes the instance's own clone or worktree (never a shared checkout or a worktree base), after looking\n" +
			"for uncommitted files and unpushed commits and refusing when it finds any (--force removes anyway), and\n" +
			"after asking (--yes does not).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			removed, err := removeInstances(ctx, cmd, s, args, workspaces, force, yes)
			if asJSON {
				if removed == nil {
					removed = []string{}
				}
				data, _ := json.Marshal(map[string][]string{"removed": removed})
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&workspaces, "workspaces", false, "also remove the instance's own git checkout, once it is known to hold no unsaved work")
	cmd.Flags().BoolVar(&force, "force", false, "remove a running instance, and checkouts that hold unsaved work")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask before removing workspaces")
	cmd.Flags().BoolVar(&asJSON, "json", false, `print {"removed": [names]}`)
	return cmd
}

// pruneFilters are the filters of `egzo prune`: an instance matches when it matches any of them.
type pruneFilters struct {
	stopped, stale bool
	olderThan      time.Duration
}

// pruneCandidate is an instance prune would remove, and why.
type pruneCandidate struct{ Name, Reason string }

// pruneCandidates picks the instances that match any filter. current is the templates the file defines,
// which stale is judged against.
func pruneCandidates(instances []stack.Instance, filters pruneFilters, current stack.Published, now time.Time) []pruneCandidate {
	var candidates []pruneCandidate
	for _, instance := range instances {
		var reasons []string
		if filters.stopped && instance.State != "running" {
			reasons = append(reasons, "stopped")
		}
		if filters.stale && instance.Stale(current) {
			reasons = append(reasons, "stale")
		}
		if filters.olderThan > 0 && now.Sub(instance.Created) > filters.olderThan {
			reasons = append(reasons, "older than "+filters.olderThan.String())
		}
		if len(reasons) > 0 {
			candidates = append(candidates, pruneCandidate{instance.Name, strings.Join(reasons, ", ")})
		}
	}
	return candidates
}

func newPruneCommand(opts *options) *cobra.Command {
	var stopped, stale, dryRun, yes, asJSON bool
	var olderThan time.Duration
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove the instances that match a filter",
		Long: "Remove the instances that match any filter given: --stopped (not running), --stale (their template changed\n" +
			"or is gone) and --older-than DURATION (created longer ago). At least one is needed. It lists what it would\n" +
			"remove and asks first (--yes does not, --dry-run only lists). Checkouts on the host are kept.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stopped && !stale && olderThan == 0 {
				return fmt.Errorf("say what to prune: --stopped, --stale or --older-than DURATION")
			}
			if olderThan < 0 {
				return fmt.Errorf("--older-than must be a positive duration like 24h")
			}
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			var current stack.Published
			if stale {
				if current, err = currentTemplates(s); err != nil {
					return err
				}
			}
			candidates := pruneCandidates(s.observed.Instances(), pruneFilters{stopped: stopped, stale: stale, olderThan: olderThan}, current, time.Now())
			names := make([]string, len(candidates))
			for i, c := range candidates {
				names[i] = c.Name
			}
			if dryRun {
				for _, c := range candidates {
					fmt.Fprintf(cmd.OutOrStdout(), "would remove %s (%s)\n", c.Name, c.Reason)
				}
				if asJSON {
					data, _ := json.Marshal(map[string][]string{"removed": []string{}})
					fmt.Fprintln(cmd.OutOrStdout(), string(data))
				}
				return nil
			}
			if len(candidates) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "nothing to prune")
				if asJSON {
					data, _ := json.Marshal(map[string][]string{"removed": []string{}})
					fmt.Fprintln(cmd.OutOrStdout(), string(data))
				}
				return nil
			}
			if !yes {
				var listing []string
				for _, c := range candidates {
					listing = append(listing, fmt.Sprintf("%s (%s)", c.Name, c.Reason))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Remove these instances?\n  %s\n[y/N] ", strings.Join(listing, "\n  "))
				answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return fmt.Errorf("aborted: nothing was removed")
				}
			}
			// What was listed and confirmed may have changed meanwhile: under the lock, only what still matches
			// every filter and was confirmed is removed.
			release, err := lockOrWait(s, cmd)
			if err != nil {
				return err
			}
			defer release()
			observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name)
			if err != nil {
				return err
			}
			confirmed := map[string]bool{}
			for _, name := range names {
				confirmed[name] = true
			}
			var still []string
			for _, c := range pruneCandidates(observed.Instances(), pruneFilters{stopped: stopped, stale: stale, olderThan: olderThan}, current, time.Now()) {
				if confirmed[c.Name] {
					still = append(still, c.Name)
				}
			}
			removed, err := removeObserved(ctx, cmd, s, observed, still, false, true, true)
			if asJSON {
				if removed == nil {
					removed = []string{}
				}
				data, _ := json.Marshal(map[string][]string{"removed": removed})
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&stopped, "stopped", false, "instances that are not running")
	cmd.Flags().BoolVar(&stale, "stale", false, "instances whose template changed or is gone")
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "instances created longer ago than this (a duration like 24h)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed and change nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask before removing")
	cmd.Flags().BoolVar(&asJSON, "json", false, `print {"removed": [names]}`)
	return cmd
}
