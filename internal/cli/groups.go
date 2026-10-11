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
	"os/exec"
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
	cmd.AddCommand(newInitCommand(), newUpCommand(opts), newDownCommand(opts), newProjectCheckCommand(opts), newRotateCACommand(opts))
	return group(cmd)
}

func newProjectCheckCommand(opts *options) *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check egzo.yaml and the secrets it names",
		Long: "Check the project: that egzo.yaml is valid, and that every secret of its vaults can be read (never printing a\n" +
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
				printWarnings(p.Warnings)
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
	report("ok", "%s is valid (project %s, %d template(s))", filepath.Base(path), p.Resolved.Name, len(p.Resolved.Agents))
	for _, warning := range p.Warnings {
		report("warn", "%s", strings.TrimPrefix(warning, "warning: "))
	}
	for _, ref := range sortedSecretRefs(p) {
		state, err := secretState(p.Resolved.Secrets[ref], p.Dir)
		if err != nil {
			return err // pass itself cannot be run: no answer about any secret means anything
		}
		if state == "set" {
			report("ok", "secret %s is set", ref)
		} else {
			report("fail", "secret %s is %s", ref, state)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Dir, ".git")); err == nil {
		if _, err := exec.LookPath("git"); err == nil {
			for _, entry := range ignored {
				check := exec.Command("git", "check-ignore", "-q", entry+"probe")
				check.Dir = p.Dir
				if check.Run() != nil {
					report("warn", "%s is not git-ignored: add `%s` to .gitignore, it holds egzo's state and the clones and worktrees of your workspaces, which must not be committed", entry, entry)
				}
			}
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
		Short: "See and refresh the project's workspaces",
	}
	cmd.AddCommand(newWorkspaceListCommand(opts), newWorkspaceFetchCommand(opts))
	return group(cmd)
}

// workspaceRow is a workspace as `workspace ls` shows it.
type workspaceRow struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Path      string   `json:"path"`
	State     string   `json:"state"`
	Worktrees []string `json:"worktrees"`
}

func workspaceRows(p *project) []workspaceRow {
	var rows []workspaceRow
	names := make([]string, 0, len(p.Resolved.Workspaces))
	for name := range p.Resolved.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ws := p.Resolved.Workspaces[name]
		row := workspaceRow{Name: name, Kind: "directory", Path: ws.Path, State: "missing", Worktrees: []string{}}
		if ws.Git == nil {
			if info, err := os.Stat(ws.Path); err == nil && info.IsDir() {
				row.State = "exists"
			}
		} else {
			row.Kind, row.State = "git", "not cloned"
			if _, err := os.Stat(filepath.Join(stack.BaseDir(ws), ".git")); err == nil {
				row.State = "cloned"
				// what the base keeps about its worktrees: one directory per worktree
				if entries, err := os.ReadDir(filepath.Join(stack.BaseDir(ws), ".git", "worktrees")); err == nil {
					for _, entry := range entries {
						if entry.IsDir() {
							row.Worktrees = append(row.Worktrees, entry.Name())
						}
					}
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func newWorkspaceListCommand(opts *options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the workspaces, with where they are and what state they are in",
		Long: "List every workspace of the project: its kind, its resolved path, and its state: a directory exists or is\n" +
			"missing, a git workspace is cloned or not, with the worktrees its base clone knows. It reads the host\n" +
			"directories only, so it works with the project down and without an engine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			printWarnings(p.Warnings)
			rows := workspaceRows(p)
			if asJSON {
				if rows == nil {
					rows = []workspaceRow{}
				}
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(rows)
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tKIND\tPATH\tSTATE\tWORKTREES")
			for _, row := range rows {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", row.Name, row.Kind, row.Path, row.State, strings.Join(row.Worktrees, ","))
			}
			return table.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the workspaces as a JSON array")
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
			user := stack.AgentUser(s.engine)
			for _, name := range names {
				if err := stack.FetchBase(ctx, s.engine, s.Resolved, s.Dir, imageRef(), user, name, cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
