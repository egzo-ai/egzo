package stack

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"

	"github.com/egzo-ai/egzo/internal/engine"
)

// PrepSpec is a short-lived container that does one job for the CLI and is removed afterwards:
// cloning a repository, handing a volume to the agent's user, checking a clone before it is deleted.
type PrepSpec struct {
	Image    string
	Cmd      []string
	User     string // "" runs as the image's user (root)
	CapAdd   []string
	Env      []string
	Mounts   []MountSpec
	Network  string // "" has no network at all
	Identity engine.Identity
	Timeout  time.Duration
}

// RunPrep runs a prep container to completion and returns what it printed. A non-zero exit is an
// error carrying that output.
func RunPrep(ctx context.Context, c *engine.Client, spec PrepSpec) (string, error) {
	if spec.Timeout == 0 {
		spec.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	config := &container.Config{Image: spec.Image, Cmd: strslice.StrSlice(spec.Cmd), User: spec.User, Env: spec.Env, Labels: spec.Identity.Labels()}
	host := &container.HostConfig{
		CapDrop:     strslice.StrSlice{"ALL"},
		CapAdd:      strslice.StrSlice(spec.CapAdd),
		SecurityOpt: []string{"no-new-privileges"},
		Init:        boolPointer(true),
	}
	if spec.Network == "" {
		host.NetworkMode = "none"
	} else {
		host.NetworkMode = container.NetworkMode(spec.Network)
	}
	for _, m := range spec.Mounts {
		kind := mount.TypeVolume
		if m.Bind {
			kind = mount.TypeBind
		}
		host.Mounts = append(host.Mounts, mount.Mount{Type: kind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	var networking *network.NetworkingConfig
	if spec.Network != "" {
		networking = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}}}
	}
	created, err := c.API.ContainerCreate(ctx, config, host, networking, nil, "")
	if err != nil {
		return "", err
	}
	defer func() {
		// the caller's context may be the thing that ended
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		c.API.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
	}()
	if err := c.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	waiting, errs := c.API.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	var exit int64
	select {
	case result := <-waiting:
		exit = result.StatusCode
	case err := <-errs:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
	output := prepLogs(context.WithoutCancel(ctx), c, created.ID)
	if exit != 0 {
		return output, fmt.Errorf("%s", strings.TrimSpace(output))
	}
	return output, nil
}

func prepLogs(ctx context.Context, c *engine.Client, id string) string {
	reader, err := c.API.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return ""
	}
	defer reader.Close()
	return demux(reader)
}

func boolPointer(b bool) *bool { return &b }

// chownVolume hands a new volume to the user agents run as.
func chownVolume(ctx context.Context, c *engine.Client, image string, spec VolumeSpec) error {
	if err := ensureImage(ctx, c, image); err != nil {
		return err
	}
	_, err := RunPrep(ctx, c, PrepSpec{
		Image:    image,
		Cmd:      []string{"/egzo", "prep", "chown", spec.Owner, "/volume"},
		CapAdd:   []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"},
		Mounts:   []MountSpec{{Source: spec.Name, Target: "/volume"}},
		Identity: engine.Identity{Project: spec.Identity.Project, Service: "prep", Kind: "prep", ProjectDir: spec.Identity.ProjectDir},
		Timeout:  time.Minute,
	})
	if err != nil {
		return fmt.Errorf("give volume %s to %s: %w", spec.Name, spec.Owner, err)
	}
	return nil
}
