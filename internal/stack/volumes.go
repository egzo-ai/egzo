// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"fmt"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// Workspaces live in engine volumes, never on the host, so everything that needs them needs only the engine.
//
//	<project>_ws.<name>              a directory workspace
//	<project>_ws.<name>.base         the base clone of a git workspace
//	<project>_ws.<name>.wt.<instance>  the worktree of an instance of a git workspace
//
// A dot cannot appear in a project, workspace or instance name, so none of these names can be another egzo
// volume's (control, ca, ca-private, egress, <agent>-home), whatever the names are.

// kindWorkspace labels the volumes of workspaces; kindLock labels the lease that is a project's lock.
const (
	kindWorkspace = "workspace"
	kindLock      = "lock"
)

// WorkspaceVolume is the volume of a directory workspace.
func WorkspaceVolume(project, workspace string) string { return project + "_ws." + workspace }

// BaseVolume is the volume of the base clone of a git workspace.
func BaseVolume(project, workspace string) string {
	return WorkspaceVolume(project, workspace) + ".base"
}

// WorktreeVolume is the volume of the worktree of an instance of a git workspace.
func WorktreeVolume(project, workspace, instance string) string {
	return WorkspaceVolume(project, workspace) + ".wt." + instance
}

// PrepEnv is what a prep container needs: the egzo image it runs and the "uid:gid" that owns the volumes (empty
// when the engine maps users itself).
type PrepEnv struct {
	Image string
	User  string
}

// workspaceIdentity labels a workspace volume: the project's own (a directory or a base), or an instance's
// worktree when from carries the instance.
func workspaceIdentity(project, dir, workspace string, from *InstanceIdentity, instance string) engine.Identity {
	id := engine.Identity{Project: project, Service: workspace, Kind: kindWorkspace, ProjectDir: dir, Workspace: workspace}
	if from != nil {
		id.Service = from.Template
		id.Instance, id.Actor, id.TemplateHash = instance, from.Actor, from.TemplateHash
	}
	return id
}

// ensureVolume creates a volume with its labels when there is none, and says whether it did. A volume of that name
// that is not the project's is somebody else's and is never adopted.
func ensureVolume(ctx context.Context, c *engine.Client, name string, id engine.Identity) (created bool, err error) {
	existing, err := c.API.VolumeInspect(ctx, name)
	switch {
	case err == nil:
		if existing.Labels[engine.LabelProject] != id.Project {
			return false, fmt.Errorf("the volume %s exists and does not belong to project %q", name, id.Project)
		}
		return false, nil
	case !cerrdefs.IsNotFound(err):
		return false, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	if _, err := c.API.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: id.Labels()}); err != nil {
		return false, fmt.Errorf("create volume %s: %w", name, err)
	}
	return true, nil
}

// ensureOwnedVolume is ensureVolume, and hands a new volume to the user agents run as: the engine makes it root's.
func ensureOwnedVolume(ctx context.Context, c *engine.Client, env PrepEnv, name string, id engine.Identity) (bool, error) {
	created, err := ensureVolume(ctx, c, name, id)
	if err != nil || !created {
		return created, err
	}
	if err := chownVolumes(ctx, c, env.Image, env.User, []string{name}, id); err != nil {
		c.API.VolumeRemove(context.WithoutCancel(ctx), name, true)
		return false, err
	}
	return true, nil
}

func volumeExists(ctx context.Context, c *engine.Client, name string) (bool, error) {
	_, err := c.API.VolumeInspect(ctx, name)
	switch {
	case err == nil:
		return true, nil
	case cerrdefs.IsNotFound(err):
		return false, nil
	}
	return false, fmt.Errorf("inspect volume %s: %w", name, err)
}

// EnsureDirectoryVolumes makes the volumes of the directory workspaces that are missing, empty and the agent user's.
// With names it makes only those; a name that is not a directory workspace of the project is ignored.
func EnsureDirectoryVolumes(ctx context.Context, c *engine.Client, env PrepEnv, project *config.Resolved, dir string, names ...string) error {
	if len(names) == 0 {
		names = sortedKeys(project.Workspaces)
	}
	sort.Strings(names)
	for _, name := range names {
		ws, ok := project.Workspaces[name]
		if !ok || ws.Git != nil {
			continue
		}
		if _, err := ensureOwnedVolume(ctx, c, env, WorkspaceVolume(project.Name, name), workspaceIdentity(project.Name, dir, name, nil, "")); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
	}
	return nil
}

// DirectoryWorkspacesOf lists the directory workspaces an agent mounts.
func DirectoryWorkspacesOf(project *config.Resolved, agent config.ResolvedAgent) []string {
	var names []string
	for _, mount := range agent.Workspaces {
		if ws, ok := project.Workspaces[mount.Name]; ok && ws.Git == nil {
			names = append(names, mount.Name)
		}
	}
	return names
}

// containersUsing lists the containers, running or not, that mount a volume.
func containersUsing(ctx context.Context, c *engine.Client, name string) ([]string, error) {
	listed, err := c.API.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("volume", name))})
	if err != nil {
		return nil, fmt.Errorf("list the containers that use %s: %w", name, err)
	}
	var names []string
	for _, item := range listed {
		if len(item.Names) > 0 {
			names = append(names, strings.TrimPrefix(item.Names[0], "/"))
		}
	}
	sort.Strings(names)
	return names, nil
}
