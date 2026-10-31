// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"github.com/egzo-ai/egzo/hub/internal/links"
	corenames "github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// A link is on somebody's machine, and all it says is data. It reaches the hub's memory, its database and people's screens only through
// here: names egzo could make, and sizes that keep a page small. The browser escapes what it shows; this is what the hub keeps.
const (
	maxAgents    = 1000
	maxTemplates = 256
	maxIgnored   = 32
	maxText      = 2000 // a status line, what claims a project
	maxWord      = 64   // a release, a state, a health, an activity
	maxHost      = 253
	maxActor     = 128
	maxMessage   = 500 // why a project was turned away
)

func word(text string, n int) string { return termsafe.Truncate(termsafe.Line(text), n) }

func validNames(list []string, limit int) []string {
	out := make([]string, 0, min(len(list), limit))
	for _, name := range list {
		if len(out) == limit {
			break
		}
		if corenames.Identifier(name) {
			out = append(out, name)
		}
	}
	return out
}

// cleanHello keeps of what a link says first what is shown to whoever approves it.
func cleanHello(hello links.Hello) links.Hello {
	hello.Host, hello.Release = word(hello.Host, maxHost), word(hello.Release, maxWord)
	hello.Templates = validNames(hello.Templates, maxTemplates)
	return hello
}

// cleanProject keeps what the hub shows of a project.
func cleanProject(p links.Project) links.Project {
	out := links.Project{Up: p.Up}
	for _, s := range p.Services {
		if (s.Name == "control" || s.Name == "proxy") && len(out.Services) < 2 {
			out.Services = append(out.Services, links.Service{Name: s.Name, State: word(s.State, maxWord), Health: word(s.Health, maxWord), ID: word(s.ID, maxWord)})
		}
	}
	for _, a := range p.Agents {
		if len(out.Agents) == maxAgents {
			break
		}
		if !corenames.Instance(a.Name) {
			continue
		}
		a.ID, a.Template, a.State, a.Health = word(a.ID, maxWord), word(a.Template, maxWord), word(a.State, maxWord), word(a.Health, maxWord)
		a.Activity, a.Status, a.Actor, a.TemplateHash = word(a.Activity, maxWord), word(a.Status, maxText), word(a.Actor, maxActor), word(a.TemplateHash, maxWord)
		out.Agents = append(out.Agents, a)
	}
	for _, text := range p.Ignored {
		if len(out.Ignored) == maxIgnored {
			break
		}
		out.Ignored = append(out.Ignored, word(text, maxText))
	}
	return out
}

// cleanStatus keeps what the hub shows of what the agents are doing.
func cleanStatus(line links.StatusLine) links.StatusLine {
	agents := make([]links.AgentStatus, 0, min(len(line.Agents), maxAgents))
	for _, a := range line.Agents {
		if len(agents) == maxAgents {
			break
		}
		if corenames.Instance(a.Agent) {
			a.Status, a.Activity = word(a.Status, maxText), word(a.Activity, maxWord)
			agents = append(agents, a)
		}
	}
	line.Agents = agents
	return line
}
