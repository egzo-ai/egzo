package stack

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Instance is an agent spawned from a template, as the engine shows it.
type Instance struct {
	Name         string
	Template     string // the key of the template, from the service label
	Actor        string
	Container    string
	State        string
	Created      time.Time
	TemplateHash string
}

// Instances lists the instances of the project, sorted by name: the containers labelled as an agent
// with an instance name.
func (o Observed) Instances() []Instance {
	var instances []Instance
	for _, r := range o.Resources {
		if r.Type == "container" && r.Kind == kindAgent && r.Instance != "" {
			instances = append(instances, Instance{
				Name: r.Instance, Template: r.Service, Actor: r.Actor, Container: r.Name,
				State: r.State, Created: r.Created, TemplateHash: r.TemplateHash,
			})
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	return instances
}

// Instance finds one instance by name.
func (o Observed) Instance(name string) (Instance, bool) {
	for _, instance := range o.Instances() {
		if instance.Name == name {
			return instance, true
		}
	}
	return Instance{}, false
}

// InstanceNames lists the names of the instances.
func (o Observed) InstanceNames() []string {
	var names []string
	for _, instance := range o.Instances() {
		names = append(names, instance.Name)
	}
	return names
}

// Stale reports whether the instance no longer matches its template: the template changed since it was
// spawned, or is gone.
func (i Instance) Stale(p Published) bool {
	template, ok := p.Templates[i.Template]
	return !ok || template.Hash != i.TemplateHash
}

// StaleInstances lists the instances that do not match the templates of p.
func StaleInstances(observed Observed, p Published) []Instance {
	var stale []Instance
	for _, instance := range observed.Instances() {
		if instance.Stale(p) {
			stale = append(stale, instance)
		}
	}
	return stale
}

// StaleError is what `up` says when stale instances stand in its way.
func StaleError(stale []Instance) error {
	names := make([]string, len(stale))
	for i, instance := range stale {
		names[i] = instance.Name
	}
	return fmt.Errorf("cannot converge while %d instance(s) are stale (their template changed or is gone): %s\n"+
		"stop them first: `egzo rm NAME...` or `egzo prune --stale`", len(stale), strings.Join(names, ", "))
}
