package stack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/volume"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/termsafe"
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

	out = &lockedWriter{w: out}
	return runConcurrently(ctx, plan, dependencies(desired, plan), func(ctx context.Context, action Action) error {
		fmt.Fprintln(out, action)
		if err := run(ctx, c, desired, action); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		return nil
	})
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
				_, err := createContainer(ctx, c, spec, desired.PrepImage)
				return err
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

// createContainer creates, starts and waits for a container. It returns the container's id as soon as
// there is one, even with an error, so that a caller that created it can remove it again.
func createContainer(ctx context.Context, c *engine.Client, spec ContainerSpec, prepImage string) (string, error) {
	config := &container.Config{
		Image:      spec.Image,
		Cmd:        strslice.StrSlice(spec.Cmd),
		User:       spec.User,
		Env:        spec.Env,
		Labels:     spec.Identity.Labels(),
		WorkingDir: spec.WorkingDir,
	}
	if len(spec.Healthcheck) > 0 {
		config.Healthcheck = healthConfig(spec.Healthcheck, c.Podman)
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode(spec.Network),
		ReadonlyRootfs: spec.ReadonlyRootfs,
		CapDrop:        strslice.StrSlice{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Runtime:        spec.Runtime,
		Init:           &spec.Init,
		Resources:      container.Resources{NanoCPUs: spec.NanoCPUs, Memory: spec.Memory, MemorySwap: spec.MemorySwap},
	}
	if spec.PidsLimit > 0 {
		limit := spec.PidsLimit
		host.Resources.PidsLimit = &limit
	}
	if spec.BoundedLogs {
		host.LogConfig = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}}
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
		if spec.Runtime != "" && strings.Contains(strings.ToLower(err.Error()), "runtime") {
			return "", fmt.Errorf("%w\nthe engine must have the %q runtime registered (Docker: \"runtimes\" in /etc/docker/daemon.json)", err, spec.Runtime)
		}
		return "", err
	}
	if err := chownVolumes(ctx, c, prepImage, spec.User, spec.OwnedVolumes, spec.Identity); err != nil {
		c.API.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return "", err
	}
	if err := c.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return created.ID, err
	}
	if err := waitHealthy(ctx, c, created.ID); err != nil {
		return created.ID, err
	}
	if spec.Identity.Kind == kindAgent {
		return created.ID, settle(ctx, c, created.ID)
	}
	return created.ID, nil
}

// settleTime is how long an agent must stay up before `up` calls it converged: a harness that
// cannot start (a bad image, a crash on its first read of the configuration) dies within moments,
// and `up` should say so instead of reporting a project whose agent is already gone.
const settleTime = 600 * time.Millisecond

func settle(ctx context.Context, c *engine.Client, id string) error {
	deadline := time.Now().Add(settleTime)
	for time.Now().Before(deadline) {
		inspected, err := c.API.ContainerInspect(ctx, id)
		if err != nil {
			return err
		}
		if !inspected.State.Running && !inspected.State.Restarting {
			return fmt.Errorf("container exited with code %d\n%s", inspected.State.ExitCode, tail(ctx, c, id))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// healthConfig probes fast until the first success so startup is not paced by the interval, then
// slowly: every probe result is written to the engine's disk. Podman's Docker-compatible API
// predates the start interval (API 1.44), so it keeps a short fixed interval.
func healthConfig(command []string, podman bool) *container.HealthConfig {
	health := &container.HealthConfig{
		Test:    append([]string{"CMD"}, command...),
		Timeout: 3 * time.Second,
	}
	if podman {
		health.Interval = time.Second
		health.Retries = 60
		return health
	}
	health.StartPeriod = healthyTimeout
	health.StartInterval = 100 * time.Millisecond
	health.Interval = 10 * time.Second
	health.Retries = 3
	return health
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
		case <-time.After(50 * time.Millisecond):
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

// ensureImage pulls image when it is not already present, with the credentials of the Docker
// configuration for its registry when it has any.
func ensureImage(ctx context.Context, c *engine.Client, ref string) error {
	if _, err := c.API.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}
	options := image.PullOptions{}
	if data, err := os.ReadFile(filepath.Join(dockerConfigDir(), "config.json")); err == nil {
		options.RegistryAuth = registryAuth(ref, data)
	}
	reader, err := c.API.ImagePull(ctx, ref, options)
	if err == nil {
		defer reader.Close()
		err = checkPullStream(reader)
	}
	if err != nil {
		return fmt.Errorf("image %s is not available locally and could not be pulled: %w\n"+
			"(sidecar images come from EGZO_IMAGE; a harness image comes from EGZO_HARNESS_PREFIX, which replaces "+
			"%q, or from the agent's image: key; a private registry needs `docker login`)", ref, err, DefaultHarnessPrefix)
	}
	return nil
}

// checkPullStream reads the progress of a pull to its end. The engine answers 200 and reports a failed
// layer or a missing manifest inside the stream, so success is the absence of an error message there.
func checkPullStream(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("unreadable pull progress: %w", err)
		}
		if message.Error != "" || message.ErrorDetail.Message != "" {
			return errors.New(strings.TrimSpace(message.Error + " " + message.ErrorDetail.Message))
		}
	}
}

func dockerConfigDir() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docker")
}

// registryAuth is the X-Registry-Auth value for the registry of ref, from a Docker config.json, or ""
// when that file has no credentials for it (credential helpers are not supported).
func registryAuth(ref string, configJSON []byte) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ""
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(configJSON, &config) != nil {
		return ""
	}
	domain := reference.Domain(named)
	for key, entry := range config.Auths {
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://"), "/")
		host, _, _ = strings.Cut(host, "/")
		if host != domain && !(domain == "docker.io" && (host == "index.docker.io" || host == "registry-1.docker.io")) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(entry.Auth)
		user, password, ok := strings.Cut(string(raw), ":")
		if err != nil || !ok {
			continue
		}
		encoded, _ := json.Marshal(map[string]string{"username": user, "password": password})
		return base64.URLEncoding.EncodeToString(encoded)
	}
	return ""
}

// Down removes a project's containers and networks, and its volumes when volumes is true.
// Workspace directories on the host are never touched.
func Down(ctx context.Context, c *engine.Client, observed Observed, volumes bool, out io.Writer) error {
	out = &lockedWriter{w: out}
	// Containers first, then the networks they were on, then the volumes. Within a kind nothing
	// depends on anything, and stopping containers is where the time goes, so do it together.
	for _, kind := range []string{"container", "network", "volume"} {
		if kind == "volume" && !volumes {
			continue
		}
		var actions []Action
		for _, r := range observed.Resources {
			if r.Type == kind {
				actions = append(actions, Action{Verb: "remove", Type: kind, Name: r.Name, ID: r.ID})
			}
		}
		deps := make([][]int, len(actions))
		err := runConcurrently(ctx, actions, deps, func(ctx context.Context, a Action) error {
			fmt.Fprintf(out, "remove %s %s\n", a.Type, a.Name)
			if err := run(ctx, c, Desired{}, a); err != nil {
				return fmt.Errorf("remove %s %s: %w", a.Type, a.Name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Report is what an agent last said about itself and what it is doing, as the control sidecar knows it.
type Report struct {
	Status   string
	Activity string
	Open     int  // requests the agent has fetched and not resolved
	Waiting  bool // it has asked a question nobody has answered
}

// Row is one container of a project as `egzo ps` shows it.
type Row struct {
	Name     string    `json:"name"`
	Service  string    `json:"service"`
	Kind     string    `json:"kind"`
	State    string    `json:"state"`
	Health   string    `json:"health"`
	Activity string    `json:"activity"`
	Open     int       `json:"open"`
	Waiting  bool      `json:"waiting"`
	Status   string    `json:"status"`
	Actor    string    `json:"actor"`
	Created  time.Time `json:"created"`
	Stale    bool      `json:"stale"`
}

// Rows describes a project's containers. reported holds what the control sidecar knows of each agent,
// by instance name; published, when the templates could be read, says which instances are stale.
func Rows(observed Observed, reported map[string]Report, published *Published) []Row {
	var rows []Row
	for _, r := range observed.Resources {
		if r.Type != "container" {
			continue
		}
		row := Row{Name: r.Name, Service: r.Service, Kind: r.Kind, State: r.State, Health: r.Health, Actor: r.Actor, Created: r.Created}
		if r.Kind == kindAgent {
			row.Name = r.Instance // what every command takes
			report := reported[r.Instance]
			row.Open, row.Waiting, row.Status = report.Open, report.Waiting, termsafe.Truncate(termsafe.Line(report.Status), 200)
			row.Activity = report.Activity
			switch {
			case r.State != "running":
				row.Activity = "stopped"
			case row.Activity == "":
				row.Activity = "starting"
			}
			if published != nil {
				row.Stale = Instance{Template: r.Service, TemplateHash: r.TemplateHash}.Stale(*published)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// WriteStatus prints a project's containers.
func WriteStatus(rows []Row, now time.Time, out io.Writer) {
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSERVICE\tSTATE\tHEALTH\tACTIVITY\tOPEN\tWAITING\tACTOR\tAGE\tSTALE\tSTATUS")
	for _, row := range rows {
		open, waiting, stale := "", "", ""
		if row.Kind == kindAgent {
			open = fmt.Sprint(row.Open)
			if row.Waiting {
				waiting = "yes"
			}
			if row.Stale {
				stale = "stale"
			}
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name, row.Service, row.State, row.Health, row.Activity, open, waiting, termsafe.Line(row.Actor), Age(now.Sub(row.Created)), stale, row.Status)
	}
	table.Flush()
}

// Age is a duration as ps shows it: the largest unit that fits, 5s, 7m, 3h, 2d.
func Age(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
