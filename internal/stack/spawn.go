package stack

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// SpawnRequest says what to spawn.
type SpawnRequest struct {
	Template string
	// Name is the instance's name; empty gives `<template>-<n>`.
	Name string
	// Actor is who asks: "operator" for the CLI, "user:<id>" or "service:<name>" for another tool.
	Actor string
}

var actorName = regexp.MustCompile(`^(operator|(user|service):[A-Za-z0-9][A-Za-z0-9._@-]{0,62})$`)

// Spawned is the instance a spawn made.
type Spawned struct {
	Name      string
	Template  string
	Container string
	Actor     string
	// TemplateHash is the hash of the template the instance was made from.
	TemplateHash string
}

// ExistsError is what a spawn answers when the name is taken by an instance.
type ExistsError struct {
	Name     string
	Template string
}

func (e *ExistsError) Error() string {
	return fmt.Sprintf("instance %q already exists (from template %q); nothing was changed", e.Name, e.Template)
}

// NotUpError means the infrastructure a spawn needs is not running.
type NotUpError struct{ Sidecar, State string }

func (e *NotUpError) Error() string {
	if e.State == "" {
		return fmt.Sprintf("the project is not up: the %s does not exist: run `egzo up` first", e.Sidecar)
	}
	return fmt.Sprintf("the project is not up: the %s is %s: run `egzo up` first", e.Sidecar, e.State)
}

// Spawn makes an instance from a template `up` published. It never reads the project file and never
// reads a secret: the proxy already holds the profiles, and the instance is only bound to one. A spawn
// that fails removes what it created, and never a checkout.
func Spawn(ctx context.Context, c *engine.Client, project, dir string, req SpawnRequest, out io.Writer) (Spawned, error) {
	observed, err := Observe(ctx, c, project)
	if err != nil {
		return Spawned{}, err
	}
	if dir != "" {
		if err := CheckOwnership(observed, project, dir); err != nil {
			return Spawned{}, err
		}
	}
	for _, name := range []string{"control", "proxy"} {
		r := observed.find("container", project+"-"+name+"-1")
		switch {
		case r == nil:
			return Spawned{}, &NotUpError{Sidecar: name}
		case r.State != "running":
			return Spawned{}, &NotUpError{Sidecar: name, State: r.State}
		}
	}
	published, err := ReadPublished(ctx, c, project)
	if err != nil {
		return Spawned{}, err
	}
	template, ok := published.Templates[req.Template]
	if !ok {
		return Spawned{}, fmt.Errorf("no template %q is published (templates: %s); a template added to egzo.yaml needs `egzo up`",
			req.Template, templateNamesList(published))
	}
	if err := refuseRuntime(c, req.Template, template.Agent.Runtime); err != nil {
		return Spawned{}, err
	}
	name := req.Name
	if name == "" {
		name = freeName(observed, req.Template)
	}
	if err := config.CheckInstanceName(name, published.Names(), published.WorkspaceNames); err != nil {
		return Spawned{}, err
	}
	if existing, ok := observed.Instance(name); ok {
		return Spawned{}, &ExistsError{Name: name, Template: existing.Template}
	}
	for _, taken := range []struct{ kind, name string }{
		{"container", InstanceContainer(project, name)}, {"network", InstanceNetwork(project, name)}, {"volume", homeVolume(project, name)},
	} {
		if r := observed.find(taken.kind, taken.name); r != nil {
			return Spawned{}, fmt.Errorf("the name %q is taken: the %s %s already exists", name, taken.kind, r.Name)
		}
	}
	if dir == "" {
		dir = published.Dir
	}
	actor := req.Actor
	if actor == "" {
		actor = "operator"
	}
	if !actorName.MatchString(actor) {
		return Spawned{}, fmt.Errorf("invalid actor %q: use operator, user:<id> or service:<name>", actor)
	}

	instances := map[string]string{name: req.Template}
	tokens, err := fetchTokens(ctx, c, project+"-control-1", []string{name})
	if err != nil {
		return Spawned{}, err
	}
	view := published.View(instances)
	desired, err := DesireInstance(view, dir, published.inputs(instances, tokens, nil), name,
		InstanceIdentity{Template: req.Template, Actor: actor, TemplateHash: template.Hash})
	if err != nil {
		return Spawned{}, err
	}
	checkouts, err := PlanGit(view)
	if err != nil {
		return Spawned{}, err
	}

	var undo undoStack
	spawned := Spawned{Name: name, Template: req.Template, Container: InstanceContainer(project, name), Actor: actor, TemplateHash: template.Hash}
	if err := spawnSteps(ctx, c, project, dir, published, view, desired, tokens, checkouts, template, name, &undo, out); err != nil {
		cleanup := context.WithoutCancel(ctx)
		undo.run(cleanup)
		// Another spawn of the same name may have won the race. The network is the arbiter: its name is the
		// first thing a spawn creates, so a spawn that created nothing lost, and says so instead of the clash.
		if again, observeErr := Observe(cleanup, c, project); observeErr == nil {
			if existing, ok := again.Instance(name); ok && undo.empty() {
				return Spawned{}, &ExistsError{Name: name, Template: existing.Template}
			}
		}
		return Spawned{}, err
	}
	return spawned, nil
}

// undoStack remembers what a spawn made, so that a failure removes exactly that.
type undoStack struct{ steps []func(context.Context) }

func (u *undoStack) add(step func(context.Context)) { u.steps = append(u.steps, step) }
func (u *undoStack) empty() bool                    { return len(u.steps) == 0 }
func (u *undoStack) run(ctx context.Context) {
	for i := len(u.steps) - 1; i >= 0; i-- {
		u.steps[i](ctx)
	}
}

func spawnSteps(
	ctx context.Context, c *engine.Client, project, dir string, published Published, view *config.Resolved, desired Desired,
	tokens map[string]string, checkouts []Checkout, template Template, name string, undo *undoStack, out io.Writer,
) error {
	out = &lockedWriter{w: out}
	do := func(action Action) error {
		fmt.Fprintln(out, action)
		if err := run(ctx, c, desired, action); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		return nil
	}
	network := desired.Networks[0]
	if err := do(Action{Verb: "create", Type: "network", Name: network.Name}); err != nil {
		return err
	}
	undo.add(func(ctx context.Context) { c.API.NetworkRemove(ctx, network.Name) })
	for _, volume := range desired.Volumes {
		if err := do(Action{Verb: "create", Type: "volume", Name: volume.Name}); err != nil {
			return err
		}
		undo.add(func(ctx context.Context) { c.API.VolumeRemove(ctx, volume.Name, true) })
	}
	for _, attachment := range desired.Attachments {
		if err := do(Action{Verb: "connect", Type: "network", Name: network.Name, Peer: attachment.Container, Alias: attachment.Alias}); err != nil {
			return err
		}
		undo.add(func(ctx context.Context) { c.API.NetworkDisconnect(ctx, network.Name, attachment.Container, true) })
	}
	if err := registerControl(ctx, c, project, name); err != nil {
		return fmt.Errorf("register %s with the control sidecar: %w", name, err)
	}
	undo.add(func(ctx context.Context) { unregisterControl(ctx, c, project, name) })
	if err := bindProxy(ctx, c, project, name, tokens[name], template.Agent.Egress); err != nil {
		return err
	}
	undo.add(func(ctx context.Context) { unbindProxy(ctx, c, project, name) })

	if len(checkouts) > 0 {
		// The clones go through the proxy as the instance, so its network and its binding come first. They
		// are never undone: a checkout holds work.
		if err := RunGitPreps(ctx, c, view, dir, published.Image, published.User, tokens, checkouts, out); err != nil {
			return err
		}
	}

	spec := desired.Containers[0]
	if err := ensureImage(ctx, c, spec.Image); err != nil {
		return err
	}
	fmt.Fprintln(out, Action{Verb: "create", Type: "container", Name: spec.Name})
	id, err := createContainer(ctx, c, spec, desired.PrepImage)
	if id != "" {
		undo.add(func(ctx context.Context) {
			timeout := stopTimeoutSeconds
			c.API.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
			c.API.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		})
	}
	if err != nil {
		return fmt.Errorf("create container %s: %w", spec.Name, err)
	}
	return nil
}

// freeName is `<template>-<n>` for the first n no instance has.
func freeName(observed Observed, template string) string {
	taken := map[string]bool{}
	for _, name := range observed.InstanceNames() {
		taken[name] = true
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%d", template, n)
		if !taken[name] {
			return name
		}
	}
}

// RemoveInstances removes instances: the container, its registration in control and in the proxy, its
// network and its home volume. Checkouts stay. Nothing is removed unless every name is an instance and,
// without force, every instance is stopped. It returns the names it removed.
func RemoveInstances(ctx context.Context, c *engine.Client, observed Observed, project string, names []string, force bool, out io.Writer) ([]string, error) {
	out = &lockedWriter{w: out}
	for _, name := range names {
		instance, ok := observed.Instance(name)
		if !ok {
			return nil, fmt.Errorf("no instance %q in this project (instances: %s)", name, strings.Join(observed.InstanceNames(), ", "))
		}
		if instance.State == "running" && !force {
			return nil, fmt.Errorf("instance %q is running: stop it first (egzo stop %s) or use --force", name, name)
		}
	}
	var removed []string
	control, proxy := observed.find("container", project+"-control-1"), observed.find("container", project+"-proxy-1")
	for _, name := range names {
		var container, network, home *Resource
		for i := range observed.Resources {
			r := &observed.Resources[i]
			switch {
			case r.Type == "container" && r.Instance == name && r.Kind == kindAgent:
				container = r
			case r.Type == "network" && r.Instance == name:
				network = r
			case r.Type == "volume" && r.Instance == name:
				home = r
			}
		}
		// The container goes first: while it runs it can still report to control, which would bring a
		// forgotten agent back.
		if container != nil {
			fmt.Fprintf(out, "remove container %s\n", container.Name)
			if err := run(ctx, c, Desired{}, Action{Verb: "remove", Type: "container", Name: container.Name, ID: container.ID}); err != nil {
				return removed, fmt.Errorf("remove container %s: %w", container.Name, err)
			}
		}
		if proxy != nil && proxy.State == "running" {
			if err := unbindProxy(ctx, c, project, name); err != nil {
				return removed, fmt.Errorf("unbind %s in the proxy: %w", name, err)
			}
		}
		if control != nil && control.State == "running" {
			if err := unregisterControl(ctx, c, project, name); err != nil {
				return removed, fmt.Errorf("unregister %s: %w", name, err)
			}
		}
		if network != nil {
			for _, sidecar := range []*Resource{control, proxy} {
				if sidecar != nil {
					c.API.NetworkDisconnect(ctx, network.ID, sidecar.ID, true)
				}
			}
			fmt.Fprintf(out, "remove network %s\n", network.Name)
			if err := c.API.NetworkRemove(ctx, network.ID); err != nil {
				return removed, fmt.Errorf("remove network %s: %w", network.Name, err)
			}
		}
		if home != nil {
			fmt.Fprintf(out, "remove volume %s\n", home.Name)
			if err := c.API.VolumeRemove(ctx, home.ID, true); err != nil {
				return removed, fmt.Errorf("remove volume %s: %w", home.Name, err)
			}
		}
		removed = append(removed, name)
	}
	return removed, nil
}
