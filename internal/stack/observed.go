// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	// Workspace is the workspace a volume belongs to.
	Workspace string
	// Instance, Actor and TemplateHash come from the labels of what belongs to an instance.
	Instance     string
	Actor        string
	TemplateHash string
	InstanceID   string    // on the container and the network of an instance
	Created      time.Time // containers only
}

// Observed is everything that exists on the engine for a project.
type Observed struct {
	Resources []Resource
	// Ignored is what carries the project's label but labels egzo never writes (engine.IdentityOf): it is not the project's, and
	// nothing is done to it. Each says what and why.
	Ignored []string
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

// resourceFrom reads a resource from its labels; an error when they are not labels egzo writes.
func resourceFrom(kind, id, name string, labels map[string]string) (Resource, error) {
	identity, err := engine.IdentityOf(labels)
	if err != nil {
		return Resource{}, fmt.Errorf("%s %s: %w", kind, name, err)
	}
	return Resource{
		Type:       kind,
		ID:         id,
		Name:       name,
		Service:    identity.Service,
		Kind:       identity.Kind,
		ConfigHash: identity.ConfigHash,
		ProjectDir: identity.ProjectDir,

		Workspace:    identity.Workspace,
		Instance:     identity.Instance,
		Actor:        identity.Actor,
		TemplateHash: identity.TemplateHash,
		InstanceID:   identity.InstanceID,
	}, nil
}

// take adds a resource read from its labels, or what is wrong with them to Ignored.
func (o *Observed) take(r Resource, err error) {
	if err != nil {
		o.Ignored = append(o.Ignored, err.Error())
		return
	}
	o.Resources = append(o.Resources, r)
}

// Observe lists every container, network and volume labelled as belonging to project.
func Observe(ctx context.Context, c *engine.Client, project string) (Observed, error) {
	var observed Observed
	f := projectFilter(project)

	containers, err := c.API.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list containers: %w", err)
	}
	// The lease that is the project's lock is the engine's business, not the project's: it never shows.
	containers = slices.DeleteFunc(containers, func(item container.Summary) bool { return item.Labels[engine.LabelKind] == kindLock })
	// One inspect per container, a few at a time: each is a round trip to the engine.
	found := make([]Resource, len(containers))
	ignored := make([]error, len(containers))
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, item := range containers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			name := ""
			if len(item.Names) > 0 {
				name = strings.TrimPrefix(item.Names[0], "/")
			}
			resource, err := resourceFrom("container", item.ID, name, item.Labels)
			if err != nil {
				ignored[i] = err
				return
			}
			resource.State = item.State
			resource.Created = time.Unix(item.Created, 0)
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
			found[i] = resource
		}()
	}
	wg.Wait()
	for i := range found {
		observed.take(found[i], ignored[i])
	}

	networks, err := c.API.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list networks: %w", err)
	}
	for _, item := range networks {
		observed.take(resourceFrom("network", item.ID, item.Name, item.Labels))
	}

	volumes, err := c.API.VolumeList(ctx, volume.ListOptions{Filters: f})
	if err != nil {
		return observed, fmt.Errorf("list volumes: %w", err)
	}
	for _, item := range volumes.Volumes {
		observed.take(resourceFrom("volume", item.Name, item.Name, item.Labels))
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
			hint := "run `egzo project down` from that directory"
			if keptByDown(r, nil) {
				hint = "run `egzo project down` and `egzo workspace rm --all` from that directory, which removes the workspace volumes `down` keeps"
			}
			return fmt.Errorf("project %q already exists from another directory (%s); "+
				"rename this project (-p, EGZO_PROJECT_NAME or name:) or %s",
				project, r.ProjectDir, hint)
		}
	}
	return nil
}
