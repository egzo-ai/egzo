// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"fmt"
	"net/http"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/egzo-ai/egzo/hub/internal/store"
)

// The templates are read from the project's spec snapshot: the resolved project file as `egzo project up` applied it. Only what the
// page shows is read, so a snapshot of a newer egzo with more keys still parses.
type snapshotFile struct {
	Config struct {
		Agents map[string]struct {
			Harness     string `yaml:"harness"`
			Image       string `yaml:"image"`
			Build       any    `yaml:"build"`
			Egress      string `yaml:"egress"`
			Workspaces  []struct{ Name, Mount, Mode string }
			Model       string `yaml:"model"`
			Permissions string `yaml:"permissions"`
			Ports       map[string]struct {
				Port     int    `yaml:"port"`
				Protocol string `yaml:"protocol"`
			} `yaml:"ports"`
		} `yaml:"agents"`
		Egress map[string]*struct {
			Allow    []string       `yaml:"allow"`
			Services map[string]any `yaml:"services"`
			SSH      map[string]any `yaml:"ssh"`
		} `yaml:"egress"`
	} `yaml:"config"`
}

type templateJSON struct {
	Name        string      `json:"name"`
	Harness     string      `json:"harness"`
	Image       string      `json:"image"`
	Built       bool        `json:"built"`
	Model       string      `json:"model"`
	Permissions string      `json:"permissions"`
	Workspaces  []mountJSON `json:"workspaces"`
	Egress      egressJSON  `json:"egress"`
	Ports       []portJSON  `json:"ports"`
}

type mountJSON struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
	Mode  string `json:"mode"`
}

type egressJSON struct {
	Profile  string   `json:"profile"`
	Allow    []string `json:"allow"`
	Services []string `json:"services"`
	SSH      []string `json:"ssh"`
}

type portJSON struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// templates answers what each template of the project gives an agent, read from its spec snapshot.
func (s *Server) templates(w http.ResponseWriter, r *http.Request, user store.User) {
	spec, err := s.hub.Spec(r.Context(), user, r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var snap snapshotFile
	if err := yaml.Unmarshal(spec, &snap); err != nil {
		s.apiFail(w, http.StatusBadGateway, "bad_spec", fmt.Sprintf("The project's spec cannot be read: %v.", err))
		return
	}
	out := make([]templateJSON, 0, len(snap.Config.Agents))
	for _, name := range sortedKeys(snap.Config.Agents) {
		a := snap.Config.Agents[name]
		t := templateJSON{Name: name, Harness: a.Harness, Image: a.Image, Built: a.Build != nil, Model: a.Model, Permissions: a.Permissions,
			Workspaces: []mountJSON{}, Ports: []portJSON{}, Egress: egressJSON{Profile: a.Egress, Allow: []string{}, Services: []string{}, SSH: []string{}}}
		for _, m := range a.Workspaces {
			t.Workspaces = append(t.Workspaces, mountJSON{Name: m.Name, Mount: m.Mount, Mode: m.Mode})
		}
		for _, p := range sortedKeys(a.Ports) {
			t.Ports = append(t.Ports, portJSON{Name: p, Port: a.Ports[p].Port, Protocol: a.Ports[p].Protocol})
		}
		if profile := snap.Config.Egress[a.Egress]; profile != nil {
			t.Egress.Allow = append(t.Egress.Allow, profile.Allow...)
			t.Egress.Services = sortedKeys(profile.Services)
			t.Egress.SSH = sortedKeys(profile.SSH)
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}
