package stack

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/egzo-ai/egzo/internal/engine"
)

// Resource is one existing engine resource that belongs to a project.
type Resource struct {
	Type       string // container, network or volume
	ID         string
	Name       string
	State      string   // containers only
	Health     string   // containers only
	Networks   []string // containers only: names of the networks it is attached to
	Service    string
	Kind       string
	ConfigHash string
	ProjectDir string
}

// Observed is everything that exists on the engine for a project.
type Observed struct {
	Resources []Resource
}

func (o Observed) find(kind, name string) *Resource {
	for i := range o.Resources {
		if o.Resources[i].Type == kind && o.Resources[i].Name == name {
			return &o.Resources[i]
		}
	}
	return nil
}

func projectFilter(project string) filters.Args {
	return filters.NewArgs(filters.Arg("label", engine.LabelProject+"="+project))
}

func resourceFrom(kind, id, name string, labels map[string]string) Resource {
	return Resource{
		Type:       kind,
		ID:         id,
		Name:       name,
		Service:    labels[engine.LabelService],
		Kind:       labels[engine.LabelKind],
		ConfigHash: labels[engine.LabelConfigHash],
		ProjectDir: labels[engine.LabelProjectDir],
	}
}

// Observe lists every container, network and volume labelled as belonging to project.
func Observe(ctx context.Context, c *engine.Client, project string) (Observed, error) {
	var observed Observed
	f := projectFilter(project)

	containers, err := c.API.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list containers: %w", err)
	}
	for _, item := range containers {
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		resource := resourceFrom("container", item.ID, name, item.Labels)
		resource.State = item.State
		if inspected, err := c.API.ContainerInspect(ctx, item.ID); err == nil {
			if inspected.State != nil && inspected.State.Health != nil {
				resource.Health = inspected.State.Health.Status
			}
			if inspected.NetworkSettings != nil {
				for networkName := range inspected.NetworkSettings.Networks {
					resource.Networks = append(resource.Networks, networkName)
				}
				sort.Strings(resource.Networks)
			}
		}
		observed.Resources = append(observed.Resources, resource)
	}

	networks, err := c.API.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list networks: %w", err)
	}
	for _, item := range networks {
		observed.Resources = append(observed.Resources, resourceFrom("network", item.ID, item.Name, item.Labels))
	}

	volumes, err := c.API.VolumeList(ctx, volume.ListOptions{Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list volumes: %w", err)
	}
	for _, item := range volumes.Volumes {
		observed.Resources = append(observed.Resources, resourceFrom("volume", item.Name, item.Name, item.Labels))
	}

	sort.Slice(observed.Resources, func(i, j int) bool {
		a, b := observed.Resources[i], observed.Resources[j]
		return a.Type+a.Name < b.Type+b.Name
	})
	return observed, nil
}

// CheckOwnership refuses to work when the project name is already used by a project that was
// created from another directory. There is no override: rename one of the projects.
func CheckOwnership(observed Observed, project, dir string) error {
	for _, r := range observed.Resources {
		if r.ProjectDir != "" && r.ProjectDir != dir {
			return fmt.Errorf("project %q already exists from another directory (%s); "+
				"rename this project (-p, EGZO_PROJECT_NAME or name:) or run `egzo down` from that directory",
				project, r.ProjectDir)
		}
	}
	return nil
}
