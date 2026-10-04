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

// loadProject reads egzo.yaml from the current directory and resolves it.
func loadProject(opts *options) (*project, error) {
	path, err := projectFile(opts)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
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
	return &project{Dir: dir, Resolved: resolved, Warnings: warnings}, nil
}

func printWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
}
