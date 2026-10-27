// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

// newProjectCommand is the group of commands about the project as a whole: its file, its infrastructure and its CA.
func newProjectCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create, bring up, check and take down the project",
	}
	cmd.AddCommand(newInitCommand(), newUpCommand(opts), newDownCommand(opts), newProjectCheckCommand(opts), newRotateCACommand(opts), newUnlockCommand(opts))
	return group(cmd)
}

func newProjectCheckCommand(opts *options) *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check egzo.yaml and the secrets it names",
		Long: "Check the project: that egzo.yaml is valid, and that every secret it refers to can be read (never printing a\n" +
			"value). Each line starts with ok, warn or fail; egzo exits non-zero when any check fails: an invalid file,\n" +
			"a secret that is missing, empty or unreadable. What `egzo project up` would change is shown, and is not an\n" +
			"error. --resolved prints the configuration with its defaults filled in and reads no secret.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resolved {
				p, err := loadProject(opts)
				if err != nil {
					return err
				}
				if missing := p.Resolved.MissingSecretKey(); missing != "" {
					return errors.New(missing)
				}
				printWarnings(append(p.Warnings, p.Resolved.UnusedVaults()...))
				encoder := yaml.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent(2)
				defer encoder.Close()
				return encoder.Encode(p.Resolved)
			}
			return checkProject(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "print the resolved configuration, with defaults filled in, and nothing else")
	return cmd
}

func checkProject(cmd *cobra.Command, opts *options) error {
	out := cmd.OutOrStdout()
	failed := false
	report := func(level, format string, a ...any) {
		if level == "fail" {
			failed = true
		}
		fmt.Fprintf(out, "%-4s %s\n", level, fmt.Sprintf(format, a...))
	}
	path, err := projectFile(opts)
	if err != nil {
		return err
	}
	p, err := loadProject(opts)
	if err != nil {
		var problems config.Errors
		if errors.As(err, &problems) {
			for _, problem := range problems {
				report("fail", "%s", problem)
			}
		} else {
			report("fail", "%s: %s", filepath.Base(path), err)
		}
		return ExitError{Code: 1}
	}
	if missing := p.Resolved.MissingSecretKey(); missing != "" {
		report("fail", "%s: %s", filepath.Base(path), missing)
		return ExitError{Code: 1}
	}
	report("ok", "%s is valid (project %s, %d template(s))", filepath.Base(path), p.Resolved.Name, len(p.Resolved.Agents))
	for _, warning := range append(p.Warnings, p.Resolved.UnusedVaults()...) {
		report("warn", "%s", strings.TrimPrefix(warning, "warning: "))
	}
	for _, ref := range sortedSecretRefs(p) {
		state, err := secretState(p.Resolved.Secrets[ref], p.Dir, ref == p.Resolved.SecretKey)
		if err != nil {
			return err // pass itself cannot be run: no answer about any secret means anything
		}
		if state == "set" {
			report("ok", "secret %s is set", ref)
		} else {
			report("fail", "secret %s is %s", ref, state)
		}
	}
	if changes, ok := pendingChanges(cmd, opts); ok && changes > 0 {
		report("warn", "the project is not up to date: %d change(s) pending, see `egzo project up --dry-run`", changes)
	}
	if failed {
		return ExitError{Code: 1}
	}
	return nil
}

// pendingChanges counts what `up` would change, when the engine can say. It is shown by `project check` and is never an error.
func pendingChanges(cmd *cobra.Command, opts *options) (int, bool) {
	ctx, stop := commandContext(cmd)
	defer stop()
	s, err := openSessionQuietly(ctx, opts)
	if err != nil {
		return 0, false
	}
	defer s.close()
	var buffer bytes.Buffer
	changes, err := dryRunUp(ctx, s, stack.Options{DryRun: true, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix)}, &buffer)
	if err != nil {
		return 0, false
	}
	return changes, true
}

// openSessionQuietly is openSession for a best-effort look at the engine: it says nothing when the engine is not there.
func openSessionQuietly(ctx context.Context, opts *options) (*session, error) {
	savedEnv, savedQuiet := opts.envWarned, opts.quiet
	opts.envWarned, opts.quiet = true, true // project check already reported the warnings
	defer func() { opts.envWarned, opts.quiet = savedEnv, savedQuiet }()
	return openSession(ctx, opts)
}

func newAgentListCommand(opts *options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the instances, without the sidecars",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			var published *stack.Published
			if p, err := currentTemplates(s); err == nil {
				published = &p
			}
			var rows []stack.Row
			for _, row := range stack.Rows(s.observed, reportedStatuses(ctx, s), published) {
				if row.Kind == "agent" {
					rows = append(rows, row)
				}
			}
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the instances as a JSON array")
	return cmd
}

// --- workspace -------------------------------------------------------------------------------------

func newWorkspaceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "See, fill, empty and refresh the project's workspaces",
		Long: "A workspace is a directory or a git repository kept in an engine volume, never on this machine: the commands\n" +
			"below are how files go in and out. Agents work in them; `git push` is how their work leaves.",
	}
	cmd.AddCommand(newWorkspaceListCommand(opts), newWorkspacePutCommand(opts), newWorkspaceGetCommand(opts),
		newWorkspaceFetchCommand(opts), newWorkspaceRemoveCommand(opts))
	return group(cmd)
}

func newWorkspaceListCommand(opts *options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the workspaces, with their volumes and what state they are in",
		Long: "List every workspace of the project: its kind, its volume, and its state: a directory exists or is missing,\n" +
			"a git workspace is cloned or not, with the worktrees of the instances. It asks the engine; the project may\n" +
			"be down.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			rows := stack.WorkspaceRows(s.Resolved, s.observed)
			if asJSON {
				if rows == nil {
					rows = []stack.WorkspaceRow{}
				}
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(rows)
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tKIND\tVOLUME\tSTATE\tWORKTREES")
			for _, row := range rows {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", row.Name, row.Kind, row.Volume, row.State, strings.Join(row.Worktrees, ","))
			}
			return table.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the workspaces as a JSON array")
	return cmd
}

// directoryWorkspace finds a declared workspace that is a directory.
func directoryWorkspace(s *session, name string) error {
	ws, ok := s.Resolved.Workspaces[name]
	switch {
	case !ok:
		return fmt.Errorf("%q is not a workspace of this project (workspaces: %s)", name, strings.Join(sortedWorkspaceNames(s.Resolved), ", "))
	case ws.Git != nil:
		return fmt.Errorf("workspace %q is a git workspace: its files are in the worktrees of the instances, which `egzo workspace get %s/INSTANCE` reads; "+
			"the agents put work back with git", name, name)
	}
	return nil
}

func sortedWorkspaceNames(p *config.Resolved) []string {
	names := make([]string, 0, len(p.Workspaces))
	for name := range p.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newWorkspacePutCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "put WORKSPACE SRC [DEST]",
		Short: "Copy a file or a directory of this machine into a directory workspace",
		Long: "Copy SRC, a file or a directory of this machine, into the directory workspace, as the agent user, so agents can\n" +
			"read and write it. DEST is the path in the workspace: for a file its name there (the file's own name by\n" +
			"default), for a directory the directory its contents go to (the directory's name by default, `.` for the\n" +
			"workspace's root). It works with the project down, and makes the workspace's volume when it is missing.",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			if err := directoryWorkspace(s, args[0]); err != nil {
				return err
			}
			dest := filepath.Base(args[1])
			if len(args) == 3 {
				dest = args[2]
			}
			return stack.PutFiles(ctx, s.engine, s.prepEnv(ctx), s.Resolved.Name, s.Dir, args[0], args[1], dest, cmd.OutOrStdout())
		},
	}
}

func newWorkspaceGetCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "get WORKSPACE[/INSTANCE] [SRC] DEST",
		Short: "Copy a path of a workspace out to this machine",
		Long: "Copy SRC, a path in the workspace (all of it by default), to DEST on this machine. A directory's contents go into\n" +
			"DEST, which is made; a file goes to DEST, or into it when it is a directory. For a git workspace, name the\n" +
			"instance: WORKSPACE/INSTANCE is that instance's worktree.",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			workspace, instance, isWorktree := strings.Cut(args[0], "/")
			ws, ok := s.Resolved.Workspaces[workspace]
			if !ok {
				return fmt.Errorf("%q is not a workspace of this project (workspaces: %s)", workspace, strings.Join(sortedWorkspaceNames(s.Resolved), ", "))
			}
			volume := stack.WorkspaceVolume(s.Resolved.Name, workspace)
			switch {
			case isWorktree && ws.Git == nil:
				return fmt.Errorf("workspace %q is a directory: there is no worktree of an instance in it", workspace)
			case isWorktree:
				volume = stack.WorktreeVolume(s.Resolved.Name, workspace, instance)
			case ws.Git != nil:
				return fmt.Errorf("workspace %q is a git workspace: name the instance, `egzo workspace get %s/INSTANCE`", workspace, workspace)
			}
			src, dest := "", args[1]
			if len(args) == 3 {
				src, dest = args[1], args[2]
			}
			return stack.GetFiles(ctx, s.engine, s.prepEnv(ctx), s.Resolved.Name, volume, src, dest, cmd.OutOrStdout())
		},
	}
}

func newWorkspaceRemoveCommand(opts *options) *cobra.Command {
	var all bool
	var worktrees []string
	cmd := &cobra.Command{
		Use:   "rm WORKSPACE... | --all | WORKSPACE --worktree INSTANCE...",
		Short: "Delete the volume of a workspace, or a worktree volume left without an instance",
		Long: "Delete the volume of each workspace: a directory's files, or a git workspace's base clone, with everything the\n" +
			"agents did not push. It is refused while an instance uses the volume, and for a git workspace while a worktree\n" +
			"of it is left. --all deletes every workspace volume. With --worktree INSTANCE, delete only that instance's\n" +
			"worktree volume, left behind when its instance is gone; the branch it made stays in the base clone.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) && len(worktrees) == 0 {
				return fmt.Errorf("name the workspaces to delete, or pass --all")
			}
			if len(worktrees) > 0 && (all || len(args) != 1) {
				return fmt.Errorf("--worktree takes exactly one workspace")
			}
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			release, err := lockOrWait(s, cmd)
			if err != nil {
				return err
			}
			defer release()
			observed, err := stack.Observe(ctx, s.engine, s.Resolved.Name)
			if err != nil {
				return err
			}
			if len(worktrees) > 0 {
				return stack.RemoveWorktreeVolumes(ctx, s.engine, s.prepEnv(ctx), observed, s.Resolved.Name, s.Dir, args[0], worktrees, cmd.OutOrStdout())
			}
			return stack.RemoveWorkspaces(ctx, s.engine, observed, args, all, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "delete every workspace volume of the project")
	cmd.Flags().StringSliceVar(&worktrees, "worktree", nil, "delete the worktree volume of this instance (repeat for several)")
	return cmd
}

func newWorkspaceFetchCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "fetch [WORKSPACE...]",
		Short: "Clone or refresh the base clones of git workspaces",
		Long: "Fetch the configured branch into the base clone of each git workspace given (all of them without names), through\n" +
			"the proxy. A base that is missing is cloned, which is the retry for a clone that failed at `egzo project up`.\n" +
			"The project must be up: the proxy holds the credentials.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			names := args
			if len(names) == 0 {
				for name, ws := range s.Resolved.Workspaces {
					if ws.Git != nil {
						names = append(names, name)
					}
				}
				sort.Strings(names)
			}
			for _, name := range names {
				if ws, ok := s.Resolved.Workspaces[name]; !ok || ws.Git == nil {
					return fmt.Errorf("%q is not a git workspace of this project", name)
				}
			}
			for _, sidecar := range []string{"control", "proxy"} {
				if r, err := s.service(sidecar); err != nil || r.State != "running" {
					return fmt.Errorf("the project is not up: run `egzo project up` first")
				}
			}
			release, err := lockOrWait(s, cmd)
			if err != nil {
				return err
			}
			defer release()
			for _, name := range names {
				if err := stack.FetchBase(ctx, s.engine, s.Resolved, s.Dir, s.prepEnv(ctx), name, cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newUnlockCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Remove the project's lock, left by a command that was killed",
		Long: "One command at a time changes a project, and the lock is a lease in the engine that the command removes when\n" +
			"it ends. A command that was killed leaves it until it expires (30 minutes); this removes it now and says who\n" +
			"held it. Do not use it while another egzo command is changing the project.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			holder, wasLocked, err := stack.Unlock(ctx, s.engine, s.Resolved.Name)
			if err != nil {
				return err
			}
			if !wasLocked {
				fmt.Fprintln(cmd.OutOrStdout(), "the project is not locked")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed the lock held by %s\n", holder)
			return nil
		},
	}
}
