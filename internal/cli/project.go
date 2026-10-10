// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egzo-ai/egzo/internal/config"
)

// project is the loaded and resolved project in the current directory.
type project struct {
	Dir      string
	Resolved *config.Resolved
	Warnings []string
}

// projectFile is the project file: -f (or EGZO_FILE) when given, otherwise egzo.yaml in this directory or
// the nearest parent, as in Docker Compose. The project directory is the directory holding it, so a
// relative path in the file means the same wherever egzo is run from.
func projectFile(opts *options) (string, error) {
	named := opts.file
	if named == "" {
		named = os.Getenv(config.EnvFile)
	}
	if named != "" {
		return filepath.Abs(named)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return config.FindFile(cwd)
}

// EnvBackendWarning is printed once per command by any command that loads a project with an env vault.
const EnvBackendWarning = "Using ENV var based secret backend is not recommended."

// loadProject reads egzo.yaml from the current directory and resolves it.
func loadProject(opts *options) (*project, error) {
	path, err := projectFile(opts)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	// The project is identified by its directory: the same directory reached through a symlink
	// (or a $PWD that is one) must be the same project.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
		path = filepath.Join(dir, filepath.Base(path))
	}
	file, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	name, err := config.ProjectName(opts.projectName, os.Getenv(config.EnvProjectName), file.Name, dir)
	if err != nil {
		return nil, err
	}
	resolved, warnings, err := config.Resolve(file, name, dir)
	if err != nil {
		return nil, err
	}
	if !opts.envWarned {
		for _, vault := range file.Vaults {
			if vault.Backend == "env" {
				opts.envWarned = true
				fmt.Fprintln(os.Stderr, EnvBackendWarning)
				break
			}
		}
	}
	return &project{Dir: dir, Resolved: resolved, Warnings: warnings}, nil
}

func printWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
}
