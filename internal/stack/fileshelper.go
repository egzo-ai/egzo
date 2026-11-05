// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"errors"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/files"
)

// kindFiles labels a file helper: a container that shows the hub one workspace volume, read only (see roadmap/hub-files.md).
const kindFiles = "files"

// The bounds of a file helper. It serves listings and downloads to a few people at a time.
const (
	filesMemory = 128 << 20
	filesPids   = 64
	filesCPUs   = 500_000_000 // half a CPU
	filesIdle   = 5 * time.Minute
)

// FilesTarget is the volume a file helper shows: a directory workspace's, or the worktree of an instance of a git workspace.
type FilesTarget struct {
	Workspace string
	Instance  string // "" for a directory workspace
}

// Volume is the volume of the target.
func (t FilesTarget) Volume(project string) string {
	if t.Instance != "" {
		return WorktreeVolume(project, t.Workspace, t.Instance)
	}
	return WorkspaceVolume(project, t.Workspace)
}

// fileHelperName is the helper's container: one per volume.
func fileHelperName(volume string) string { return volume + "-files" }

// FilesConnectCommand is what the link runs in a file helper for one connection.
var FilesConnectCommand = []string{"/egzo", "files", "connect", "--announce"}

// FileHelper finds the running file helper of a workspace volume, or makes one, and returns its container's id. The volume must be one of
// the project's workspace volumes that holds files: a directory, or a worktree (a base clone has none checked out).
//
// The helper runs the egzo image as the agents' user, with the volume read only at /w, no network, a read-only root and no capability,
// and bounded memory, pids and CPU. It removes itself when its server has been idle for five minutes.
//
// env is asked only when a helper has to be made: it costs a question to the control sidecar.
func FileHelper(ctx context.Context, c *engine.Client, env func(context.Context) (PrepEnv, error), project string, target FilesTarget) (string, error) {
	volume := target.Volume(project)
	inspected, err := c.API.VolumeInspect(ctx, volume)
	switch {
	case cerrdefs.IsNotFound(err):
		return "", fmt.Errorf("there is no volume %s", volume)
	case err != nil:
		return "", err
	}
	identity, err := engine.IdentityOf(inspected.Labels)
	if err != nil || identity.Project != project || identity.Kind != kindWorkspace || identity.Workspace != target.Workspace || identity.Instance != target.Instance {
		return "", fmt.Errorf("the volume %s is not a workspace of project %s", volume, project)
	}
	name := fileHelperName(volume)
	deadline := time.Now().Add(filesStarting)
	for {
		id, starting, err := runningHelper(ctx, c, name)
		if err != nil || id != "" {
			return id, err
		}
		if !starting {
			prep, err := env(ctx)
			if err != nil {
				return "", err
			}
			id, err = startFileHelper(ctx, c, prep, project, target, volume, name)
			if err == nil || !cerrdefs.IsConflict(err) && !cerrdefs.IsNotFound(err) {
				return id, err
			}
			// another connection made it at the same moment, or the last one is still removing itself
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the file helper of %s did not start", volume)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// filesStarting is how long a helper that another connection made may take to start before it is taken for one that never will.
const filesStarting = 15 * time.Second

// runningHelper returns the id of the helper if it runs. One that another connection made and is starting is waited for: starting says
// so. One that does not run otherwise (it exited and is being removed, it was stopped, or it was made and never started) is removed.
func runningHelper(ctx context.Context, c *engine.Client, name string) (id string, starting bool, err error) {
	inspected, err := c.API.ContainerInspect(ctx, name)
	switch {
	case cerrdefs.IsNotFound(err):
		return "", false, nil
	case err != nil:
		return "", false, err
	case inspected.State == nil:
	case inspected.State.Running:
		return inspected.ID, false, nil
	case inspected.State.Status == container.StateRemoving:
		return "", true, nil
	case inspected.State.Status == container.StateCreated:
		if made, err := time.Parse(time.RFC3339Nano, inspected.Created); err == nil && time.Since(made) < filesStarting {
			return "", true, nil
		}
	}
	if err := c.API.ContainerRemove(ctx, inspected.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
		return "", false, err
	}
	return "", false, nil
}

func startFileHelper(ctx context.Context, c *engine.Client, env PrepEnv, project string, target FilesTarget, volume, name string) (string, error) {
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return "", err
	}
	identity := engine.Identity{Project: project, Service: kindFiles, Kind: kindFiles, Workspace: target.Workspace, Instance: target.Instance}
	config := &container.Config{
		Image:  env.Image,
		Cmd:    strslice.StrSlice{"/egzo", "files", "serve", "--root", "/w", "--socket", files.Socket, "--idle", filesIdle.String()},
		User:   env.User,
		Labels: identity.Labels(),
	}
	pids := int64(filesPids)
	host := &container.HostConfig{
		NetworkMode:    "none",
		Mounts:         []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/w", ReadOnly: true}},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=1m,mode=1777"},
		CapDrop:        strslice.StrSlice{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		AutoRemove:     true,
		Init:           boolPointer(true),
		Resources:      container.Resources{Memory: filesMemory, PidsLimit: &pids, NanoCPUs: filesCPUs},
	}
	created, err := c.API.ContainerCreate(ctx, config, host, nil, nil, name)
	if err != nil {
		return "", err
	}
	if err := c.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		c.API.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start the file helper of %s: %w", volume, err)
	}
	return created.ID, nil
}

// RemoveFileHelpers removes the file helpers that mount any of volumes, before the volumes are deleted: a volume a container mounts cannot
// be. A person browsing it sees the connection end.
func RemoveFileHelpers(ctx context.Context, c *engine.Client, project string, volumes []string) error {
	var failed []error
	for _, volume := range volumes {
		// one that exited is removing itself: wait for it, or the volume is still in use
		for wait := 0; ; wait++ {
			listed, err := fileHelpersOf(ctx, c, project, volume)
			if err != nil {
				return err
			}
			if len(listed) == 0 {
				break
			}
			if wait == 50 {
				failed = append(failed, fmt.Errorf("the file helper of %s is still there", volume))
				break
			}
			for _, helper := range listed {
				err := c.API.ContainerRemove(ctx, helper.ID, container.RemoveOptions{Force: true})
				if err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
					return fmt.Errorf("remove the file helper of %s: %w", volume, err)
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return errors.Join(failed...)
}

func fileHelpersOf(ctx context.Context, c *engine.Client, project, volume string) ([]container.Summary, error) {
	listed, err := c.API.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"="+kindFiles), filters.Arg("volume", volume))})
	if err != nil {
		return nil, fmt.Errorf("list the file helpers of %s: %w", volume, err)
	}
	return listed, nil
}
