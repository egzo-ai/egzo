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

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	var attach, wait, asJSON, reuse, noFetch bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "spawn TEMPLATE [NAME]",
		Short: "Start an agent from a template",
		Long: "Start an agent from one of the templates `egzo up` published (the agents of egzo.yaml). NAME is the\n" +
			"instance's name, `<template>-<n>` when left out. -m sends it a first message, the task. A name that\n" +
			"is taken fails with exit code 17 and changes nothing. Each git workspace the template lists gives the\n" +
			"instance a worktree of its base clone on a branch of its own, egzo/NAME, made from a fresh fetch\n" +
			"(--no-fetch skips it; a fetch that fails only warns). A worktree or a branch that is there already\n" +
			"fails the spawn; --reuse continues with it.\n" +
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

			request := stack.SpawnRequest{Template: args[0], Actor: operatorActor, NoFetch: noFetch, Reuse: reuse}
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
	cmd.Flags().BoolVar(&reuse, "reuse", false, "continue with the worktree or the branch an earlier instance of this name left")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "make the worktrees from the base clones as they are, without fetching first")
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

// removeInstances removes the instances named, and with them their worktrees. Nothing is removed unless
// everything may be.
func removeInstances(ctx context.Context, cmd *cobra.Command, s *session, names []string, force bool) ([]string, error) {
	release, err := lockOrWait(s, cmd)
	if err != nil {
		return nil, err
	}
	defer release()
	observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name)
	if err != nil {
		return nil, err
	}
	return removeObserved(ctx, cmd, s, observed, names, force)
}

// removeObserved is removeInstances under the lock, for what was just observed. An instance's worktree is
// its own and goes with it, with no check for work in it: what was committed stays on the branch egzo/NAME
// in the base clone.
func removeObserved(ctx context.Context, cmd *cobra.Command, s *session, observed stack.Observed, names []string, force bool) ([]string, error) {
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
	}
	removed, err := stack.RemoveInstances(ctx, s.engine, observed, s.Resolved.Name, names, true, cmd.ErrOrStderr())
	if werr := stack.RemoveWorktrees(s.Resolved, removed, cmd.ErrOrStderr()); err == nil {
		err = werr
	}
	return removed, err
}

func newRmCommand(opts *options) *cobra.Command {
	var force, asJSON bool
	cmd := &cobra.Command{
		Use:   "rm INSTANCE...",
		Short: "Remove instances",
		Long: "Remove instances: the container, the network, the home volume and the worktrees of its git workspaces,\n" +
			"and the agent's registration in the control sidecar and the proxy. What was asked of the agent and is\n" +
			"still open is closed as failed. A running instance needs --force. Nothing is checked first: what the\n" +
			"agent committed stays on its branch egzo/NAME in the base clone, the rest of a worktree is gone.\n" +
			"Directory workspaces are never touched.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			removed, err := removeInstances(ctx, cmd, s, args, force)
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
	cmd.Flags().BoolVar(&force, "force", false, "remove a running instance")
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
			"remove and asks first (--yes does not, --dry-run only lists). Removing an instance removes its worktrees.",
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
			removed, err := removeObserved(ctx, cmd, s, observed, still, true)
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
