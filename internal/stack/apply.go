package stack

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/egzo-ai/egzo/internal/engine"
)

const (
	stopTimeoutSeconds = 5
	healthyTimeout     = 90 * time.Second
)

// Apply executes a plan. With dryRun it only reports what it would do.
func Apply(ctx context.Context, c *engine.Client, desired Desired, plan []Action, dryRun bool, out io.Writer) error {
	if len(plan) == 0 {
		return nil
	}
	if dryRun {
		for _, action := range plan {
			fmt.Fprintf(out, "would %s\n", action)
		}
		return nil
	}

	creating := map[string]bool{}
	for _, action := range plan {
		if action.Type == "container" && action.Verb == "create" {
			creating[action.Name] = true
		}
	}
	pulled := map[string]bool{}
	for _, spec := range desired.Containers {
		if creating[spec.Name] && !pulled[spec.Image] {
			pulled[spec.Image] = true
			if err := ensureImage(ctx, c, spec.Image); err != nil {
				return err
			}
		}
	}

	for _, action := range plan {
		fmt.Fprintln(out, action)
		if err := run(ctx, c, desired, action); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	return nil
}

func run(ctx context.Context, c *engine.Client, desired Desired, action Action) error {
	switch action.Verb + " " + action.Type {
	case "remove container":
		return removeContainer(ctx, c, action.ID)
	case "remove network":
		return c.API.NetworkRemove(ctx, action.ID)
	case "remove volume":
		return c.API.VolumeRemove(ctx, action.ID, true)
	case "create network":
		for _, spec := range desired.Networks {
			if spec.Name == action.Name {
				_, err := c.API.NetworkCreate(ctx, spec.Name, network.CreateOptions{
					Driver: "bridge", Internal: spec.Internal, Labels: spec.Identity.Labels(),
				})
				return err
			}
		}
	case "create volume":
		for _, spec := range desired.Volumes {
			if spec.Name == action.Name {
				_, err := c.API.VolumeCreate(ctx, volume.CreateOptions{Name: spec.Name, Labels: spec.Identity.Labels()})
				return err
			}
		}
	case "create container":
		for _, spec := range desired.Containers {
			if spec.Name == action.Name {
				return createContainer(ctx, c, spec)
			}
		}
	case "connect network":
		return c.API.NetworkConnect(ctx, action.Name, action.Peer, &network.EndpointSettings{Aliases: []string{action.Alias}})
	case "disconnect network":
		return c.API.NetworkDisconnect(ctx, action.ID, action.Peer, true)
	case "start container":
		if err := c.API.ContainerStart(ctx, action.ID, container.StartOptions{}); err != nil {
			return err
		}
		return waitHealthy(ctx, c, action.ID)
	}
	return fmt.Errorf("no such action")
}

func removeContainer(ctx context.Context, c *engine.Client, id string) error {
	timeout := stopTimeoutSeconds
	_ = c.API.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	return c.API.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

func createContainer(ctx context.Context, c *engine.Client, spec ContainerSpec) error {
	config := &container.Config{
		Image:      spec.Image,
		Cmd:        strslice.StrSlice(spec.Cmd),
		Env:        spec.Env,
		Labels:     spec.Identity.Labels(),
		WorkingDir: spec.WorkingDir,
	}
	if len(spec.Healthcheck) > 0 {
		config.Healthcheck = &container.HealthConfig{
			Test:     append([]string{"CMD"}, spec.Healthcheck...),
			Interval: time.Second,
			Timeout:  3 * time.Second,
			Retries:  60,
		}
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode(spec.Network),
		ReadonlyRootfs: spec.ReadonlyRootfs,
		CapDrop:        strslice.StrSlice{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Runtime:        spec.Runtime,
		Init:           &spec.Init,
		Resources:      container.Resources{NanoCPUs: spec.NanoCPUs, Memory: spec.Memory},
	}
	host.Tmpfs = spec.Tmpfs
	for _, m := range spec.Mounts {
		kind := mount.TypeVolume
		if m.Bind {
			kind = mount.TypeBind
		}
		host.Mounts = append(host.Mounts, mount.Mount{Type: kind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	networking := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}}}

	created, err := c.API.ContainerCreate(ctx, config, host, networking, nil, spec.Name)
	if err != nil {
		return err
	}
	if err := c.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return err
	}
	return waitHealthy(ctx, c, created.ID)
}

// waitHealthy blocks until the container is healthy: converged means ready, not just started.
func waitHealthy(ctx context.Context, c *engine.Client, id string) error {
	deadline := time.Now().Add(healthyTimeout)
	for time.Now().Before(deadline) {
		inspected, err := c.API.ContainerInspect(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case !inspected.State.Running && !inspected.State.Restarting:
			return fmt.Errorf("container exited with code %d\n%s", inspected.State.ExitCode, tail(ctx, c, id))
		case inspected.State.Health == nil && inspected.State.Running:
			return nil
		case inspected.State.Health != nil && inspected.State.Health.Status == "healthy":
			return nil
		case inspected.State.Health != nil && inspected.State.Health.Status == "unhealthy":
			return fmt.Errorf("container is unhealthy\n%s", tail(ctx, c, id))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("container not healthy after %s\n%s", healthyTimeout, tail(ctx, c, id))
}

func tail(ctx context.Context, c *engine.Client, id string) string {
	logs, err := c.API.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: "20"})
	if err != nil {
		return ""
	}
	defer logs.Close()
	data, _ := io.ReadAll(logs)
	return strings.TrimSpace(string(data))
}

// ensureImage pulls image when it is not already present.
func ensureImage(ctx context.Context, c *engine.Client, ref string) error {
	if _, err := c.API.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !client.IsErrNotFound(err) {
		return err
	}
	reader, err := c.API.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("image %s is not available locally and could not be pulled (set EGZO_IMAGE to use another image): %w", ref, err)
	}
	defer reader.Close()
	_, err = io.Copy(io.Discard, reader)
	return err
}

// Down removes a project's containers and networks, and its volumes when volumes is true.
// Workspace directories on the host are never touched.
func Down(ctx context.Context, c *engine.Client, observed Observed, volumes bool, out io.Writer) error {
	for _, kind := range []string{"container", "network", "volume"} {
		if kind == "volume" && !volumes {
			continue
		}
		for _, r := range observed.Resources {
			if r.Type != kind {
				continue
			}
			fmt.Fprintf(out, "remove %s %s\n", r.Type, r.Name)
			var err error
			switch kind {
			case "container":
				err = removeContainer(ctx, c, r.ID)
			case "network":
				err = c.API.NetworkRemove(ctx, r.ID)
			case "volume":
				err = c.API.VolumeRemove(ctx, r.ID, true)
			}
			if err != nil {
				return fmt.Errorf("remove %s %s: %w", r.Type, r.Name, err)
			}
		}
	}
	return nil
}

// WriteStatus prints a project's containers. reported holds what each agent last said about itself.
func WriteStatus(observed Observed, reported map[string]string, out io.Writer) {
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSERVICE\tSTATE\tHEALTH\tSTATUS")
	for _, r := range observed.Resources {
		if r.Type == "container" {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Service, r.State, r.Health, reported[r.Service])
		}
	}
	table.Flush()
}
