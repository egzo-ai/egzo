package cli

import (
	"fmt"
	"os"

	"github.com/egzo-ai/egzo/internal/config"
)

// project is the loaded and resolved project in the current directory.
type project struct {
	Dir      string
	Resolved *config.Resolved
	Warnings []string
}

// loadProject reads egzo.yaml from the current directory and resolves it.
func loadProject(opts *options) (*project, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	file, err := config.Load(dir)
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
