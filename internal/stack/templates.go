// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// publishedVersion is bumped when what Published holds changes in a way an older spawn cannot read.
const publishedVersion = 1

// Template is one agent of the file as a spawn instantiates it: everything that decides what the
// instance is, resolved by `up`, so that a spawn needs neither the file nor any secret.
type Template struct {
	Agent      config.ResolvedAgent
	Profile    *config.ResolvedProfile
	Workspaces map[string]config.ResolvedWorkspace // the declared workspaces its mounts name
	// PromptText is the text of the agent's prompt file, read when `up` ran: an instance needs nothing on the host.
	PromptText string
	// Image is the harness image, resolved when `up` ran, or the image it built.
	Image string
	// LocalPorts and Hub are what the project says about where a declared port can open, for a template that declares ports: the instance is
	// told them, and a change of either makes its instances stale. A template without ports does not depend on them.
	LocalPorts *config.ResolvedLocalPorts `json:",omitempty"`
	Hub        bool                       `json:",omitempty"`
	// Placeholders maps each variable the agent gets to its placeholder text. A placeholder is not a
	// secret. The text is made once and never changes, so the hash leaves it out: what decides whether
	// an instance is stale is the profile's service, which names the variable and the secret.
	Placeholders map[string]string `json:",omitempty"`
	// Hash fingerprints all of the above. An instance carries the hash of the template it was spawned
	// from; when the template's hash moves, the instance is stale.
	Hash string
}

// Published is what `up` leaves in the control sidecar for spawn: the templates, and what is needed to
// run an instance as `up` would have run it.
type Published struct {
	Version int
	Project string
	Dir     string
	// Image is the all-in-one egzo image: the prep containers use it.
	Image string
	// User is the "uid:gid" instances run as: the user who ran `up`.
	User      string
	Templates map[string]Template
}

// Publish resolves the templates of a project. It reads the prompt files: the text travels in the template, so a
// spawn needs no file, and a changed prompt makes its instances stale.
func Publish(project *config.Resolved, dir, image, harnessPrefix, user string) (Published, error) {
	return PublishWith(project, dir, image, harnessPrefix, user, nil)
}

// PublishWith is Publish with the placeholders (by secret reference) the agents get.
func PublishWith(project *config.Resolved, dir, image, harnessPrefix, user string, placeholders map[string]string) (Published, error) {
	prompts, err := ReadPrompts(project, dir)
	if err != nil {
		return Published{}, err
	}
	published := Published{Version: publishedVersion, Project: project.Name, Dir: dir, Image: image, User: user, Templates: map[string]Template{}}
	for _, name := range sortedKeys(project.Agents) {
		agent := project.Agents[name]
		image := agentImage(agent, harnessPrefix)
		if agent.Build != nil {
			// the tag `up` builds: it covers the context, so a changed file changes the template
			plan, err := planBuild(project.Name, dir, name, agent, harnessPrefix)
			if err != nil {
				return Published{}, err
			}
			image = plan.Tag
		}
		template := Template{
			Agent:        agent,
			Profile:      project.Egress[agent.Egress],
			Workspaces:   map[string]config.ResolvedWorkspace{},
			PromptText:   prompts[name],
			Image:        image,
			Placeholders: templatePlaceholders(project.Egress[agent.Egress], placeholders),
		}
		if len(agent.Ports) > 0 {
			template.LocalPorts, template.Hub = project.LocalPorts, project.Hub != nil
		}
		template.Agent.Prompt = "" // the text is in PromptText: the path means nothing to a spawn
		for _, mount := range agent.Workspaces {
			template.Workspaces[mount.Name] = project.Workspaces[mount.Name]
		}
		template.Hash = templateHash(template)
		published.Templates[name] = template
	}
	return published, nil
}

func templateHash(template Template) string {
	template.Hash = ""
	template.Placeholders = nil
	return hash(template)
}

// Names lists the templates, sorted.
func (p Published) Names() []string { return sortedKeys(p.Templates) }

// Marshal is the form the templates are stored in.
func (p Published) Marshal() ([]byte, error) { return json.Marshal(p) }

// ParsePublished reads what Marshal wrote.
func ParsePublished(data []byte) (Published, error) {
	var p Published
	if err := json.Unmarshal(data, &p); err != nil {
		return Published{}, fmt.Errorf("the published templates are unreadable: %w", err)
	}
	if p.Version != publishedVersion {
		return Published{}, fmt.Errorf("the published templates are version %d, this egzo reads version %d: run `egzo project up` with this version", p.Version, publishedVersion)
	}
	return p, nil
}

// View is the project as one or more instances see it: the agents are the given instances (name to
// template), each a copy of its template under its own name, with the image resolved. The functions that compute an agent's resources work on it unchanged.
func (p Published) View(instances map[string]string) *config.Resolved {
	view := &config.Resolved{
		Name:       p.Project,
		Workspaces: map[string]config.ResolvedWorkspace{},
		Agents:     map[string]config.ResolvedAgent{},
		Egress:     map[string]*config.ResolvedProfile{},
	}
	for name, templateName := range instances {
		template, ok := p.Templates[templateName]
		if !ok {
			continue
		}
		agent := template.Agent
		agent.Image = template.Image
		view.Agents[name] = agent
		if template.LocalPorts != nil {
			view.LocalPorts = template.LocalPorts
		}
		if template.Hub {
			view.Hub = &config.ResolvedHub{} // only to say that a hub is named: an instance has no use for its address
		}
		for key, ws := range template.Workspaces {
			view.Workspaces[key] = ws
		}
		if template.Profile != nil {
			view.Egress[agent.Egress] = template.Profile
		}
	}
	return view
}

// AsProject is the project as the published templates describe it, each template an agent under its own
// name: what restarting the proxy builds its policy from, so a file edited since `up` changes nothing.
// The secrets and the secret key are the file's (nil for none), since the templates hold references and never where a
// secret lives.
func (p Published) AsProject(file *config.Resolved) *config.Resolved {
	instances := map[string]string{}
	for name := range p.Templates {
		instances[name] = name
	}
	view := p.View(instances)
	view.Hub, view.LocalPorts = nil, nil
	if file != nil {
		view.Secrets, view.SecretKey = file.Secrets, file.SecretKey
		if file.Proxy != nil && file.Proxy.Upstream != nil {
			view.Proxy = &config.ResolvedProxy{Upstream: file.Proxy.Upstream} // for the policy: the image is not the view's
		}
	}
	return view
}

// prompts maps instances to the prompt text of their template.
func (p Published) prompts(instances map[string]string) map[string]string {
	out := map[string]string{}
	for name, templateName := range instances {
		if text := p.Templates[templateName].PromptText; text != "" {
			out[name] = text
		}
	}
	return out
}

// inputs is what DesireInstance needs besides the view.
func (p Published) inputs(instances map[string]string, tokens map[string]string, existing []string) Inputs {
	placeholders := map[string]map[string]string{}
	for name, templateName := range instances {
		if vars := p.Templates[templateName].Placeholders; len(vars) > 0 {
			placeholders[name] = vars
		}
	}
	return Inputs{Image: p.Image, Tokens: tokens, User: p.User, Prompts: p.prompts(instances), Instances: existing, Placeholders: placeholders}
}

// ErrNotPublished means `up` has not published templates yet.
var ErrNotPublished = errors.New("no templates are published: run `egzo project up` first")

// ReadPublished asks the control sidecar for the templates `up` last published.
func ReadPublished(ctx context.Context, c *engine.Client, project string) (Published, error) {
	data, err := ControlRequest(ctx, c, project, "GET", "/v1/templates", nil)
	if err != nil {
		if strings.Contains(err.Error(), "no templates published") {
			return Published{}, ErrNotPublished
		}
		return Published{}, err
	}
	return ParsePublished(data)
}

// publishTemplates stores the templates in the control sidecar unless it already holds exactly these.
func publishTemplates(ctx context.Context, c *engine.Client, p Published, controlName string, fresh bool, out io.Writer) (bool, error) {
	data, err := p.Marshal()
	if err != nil {
		return false, err
	}
	if !fresh {
		current, err := c.Exec(ctx, controlName, []string{"/egzo", "control", "request", "GET", "/v1/templates"}, nil)
		if err != nil {
			return false, fmt.Errorf("check the published templates: %w", err)
		}
		if current.ExitCode == 0 && bytes.Equal(bytes.TrimSpace(current.Stdout), data) {
			return false, nil
		}
	}
	result, err := c.Exec(ctx, controlName, []string{"/egzo", "control", "request", "PUT", "/v1/templates"}, bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("publish the templates: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("publish the templates: %s", strings.TrimSpace(string(result.Stderr)))
	}
	fmt.Fprintf(out, "publish templates %s\n", strings.Join(p.Names(), ", "))
	return true, nil
}

// templateNamesList is for messages.
func templateNamesList(p Published) string {
	names := p.Names()
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
