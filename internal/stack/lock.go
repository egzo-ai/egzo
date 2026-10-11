// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/version"
)

// The project lock: one command at a time changes a project. Two `up` runs at once would both create the same
// containers, and a `down` would remove what an `up` is making.
//
// It is a lease held in the engine, so any client of the engine can take it, whatever machine it runs on: a
// container named <project>.lock, created and never started. Creating a container under a name is atomic, so one
// command wins. Labels say who holds it and until when. The holder removes the container when it is done; a
// command that was killed leaves the lease until it expires, or until `egzo project unlock` removes it.
//
// The dot in the name cannot appear in a project, template or instance name, so no resource of the project has it.

// LeaseTTL is how long a lease stays valid. A command does not renew it: it is far longer than any command takes.
const LeaseTTL = 30 * time.Minute

// LockName is the name of the lease container of a project.
func LockName(project string) string { return project + ".lock" }

// Holder is who takes the lease, as it is written on it. The hub says which user.
var Holder = func() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("egzo %s, pid %d on %s", version.Version, os.Getpid(), host)
}

// LockedError is what a command that cannot take the lock says.
type LockedError struct {
	Project string
	Holder  string
	Expires time.Time
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("another egzo command is changing this project right now (held by %s, until %s): wait for it to finish; "+
		"if that command was killed, `egzo project unlock` removes its lock", e.Holder, e.Expires.Local().Format("15:04:05"))
}

// Lock takes the project's lock, or fails with a *LockedError when another command holds it. image is any image the
// engine has or can pull: the egzo image.
func Lock(ctx context.Context, c *engine.Client, project, image string) (release func(), err error) {
	return lock(ctx, c, project, image, 0)
}

// LockWait is Lock for commands that are meant to run side by side, such as several spawns at once: when another
// command holds the lock it waits for it, up to the timeout, and then fails like Lock.
func LockWait(ctx context.Context, c *engine.Client, project, image string, timeout time.Duration) (release func(), err error) {
	return lock(ctx, c, project, image, timeout)
}

func lock(ctx context.Context, c *engine.Client, project, image string, wait time.Duration) (func(), error) {
	if err := ensureImage(ctx, c, image); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		id, err := takeLease(ctx, c, project, image)
		if err == nil {
			return func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				// by id: a lease that expired and was taken over is somebody else's now, and is not ours to remove
				c.API.ContainerRemove(cleanup, id, container.RemoveOptions{Force: true})
			}, nil
		}
		var locked *LockedError
		if !asLocked(err, &locked) || !time.Now().Before(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func asLocked(err error, target **LockedError) bool {
	locked, ok := err.(*LockedError)
	if ok {
		*target = locked
	}
	return ok
}

// takeLease creates the lease container, replacing one that has expired.
func takeLease(ctx context.Context, c *engine.Client, project, image string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		expires := time.Now().Add(LeaseTTL)
		id := engine.Identity{Project: project, Service: "lock", Kind: kindLock}
		labels := id.Labels()
		labels[engine.LabelLockHolder] = Holder()
		labels[engine.LabelLockExpires] = strconv.FormatInt(expires.Unix(), 10)
		created, err := c.API.ContainerCreate(ctx,
			&container.Config{Image: image, Cmd: []string{"/egzo", "version"}, Labels: labels},
			&container.HostConfig{NetworkMode: "none"}, nil, nil, LockName(project))
		if err == nil {
			return created.ID, nil
		}
		if !cerrdefs.IsConflict(err) {
			return "", fmt.Errorf("take the project lock: %w", err)
		}
		leaseID, holder, until, found, inspectErr := readLease(ctx, c, project)
		if inspectErr != nil {
			return "", inspectErr
		}
		if !found {
			continue // released between the two calls
		}
		if time.Now().Before(until) {
			return "", &LockedError{Project: project, Holder: holder, Expires: until}
		}
		// expired: the command that held it is gone. Removed by the id that was read: another command may have
		// replaced it since, and that lease is live.
		if err := c.API.ContainerRemove(ctx, leaseID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return "", fmt.Errorf("take over the expired project lock: %w", err)
		}
	}
	return "", fmt.Errorf("take the project lock: the lease keeps changing hands")
}

func readLease(ctx context.Context, c *engine.Client, project string) (id, holder string, until time.Time, found bool, err error) {
	inspected, err := c.API.ContainerInspect(ctx, LockName(project))
	if cerrdefs.IsNotFound(err) {
		return "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("inspect the project lock: %w", err)
	}
	seconds, _ := strconv.ParseInt(inspected.Config.Labels[engine.LabelLockExpires], 10, 64)
	return inspected.ID, inspected.Config.Labels[engine.LabelLockHolder], time.Unix(seconds, 0), true, nil
}

// Unlock removes the project's lock whoever holds it, and says who did. It is for a command that was killed.
func Unlock(ctx context.Context, c *engine.Client, project string) (holder string, wasLocked bool, err error) {
	_, holder, _, found, err := readLease(ctx, c, project)
	if err != nil || !found {
		return "", false, err
	}
	if err := c.API.ContainerRemove(ctx, LockName(project), container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return holder, true, fmt.Errorf("remove the project lock: %w", err)
	}
	return holder, true, nil
}
