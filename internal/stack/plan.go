package stack

import "fmt"

// Action is one step of converging a project.
type Action struct {
	Verb string // create, remove, start, connect, disconnect
	Type string // container, network, volume
	Name string
	ID   string // existing resource, for remove, start and disconnect
	// Peer is the container a connect or disconnect action moves in or out of the network Name;
	// Alias is the name it is reachable by on that network.
	Peer  string
	Alias string
}

func (a Action) String() string {
	if a.Peer != "" {
		preposition := map[string]string{"connect": "to", "disconnect": "from"}[a.Verb]
		return fmt.Sprintf("%s network %s %s %s", a.Verb, a.Name, preposition, a.Peer)
	}
	return fmt.Sprintf("%s %s %s", a.Verb, a.Type, a.Name)
}

// BuildPlan compares the desired resources with what exists. The returned actions are in
// execution order: containers go first when removing and last when creating.
//
// Volumes hold data, so they are never removed or recreated by up: only `down --volumes` deletes them.
func BuildPlan(desired Desired, observed Observed, recreate bool) []Action {
	var removeContainers, disconnects, removeNetworks, createNetworks, createVolumes, createContainers, starts, connects []Action
	wanted := map[string]bool{}
	removed := map[string]bool{} // containers going away or being recreated

	for _, spec := range desired.Networks {
		wanted["network/"+spec.Name] = true
		switch existing := observed.find("network", spec.Name); {
		case existing == nil:
			createNetworks = append(createNetworks, Action{Verb: "create", Type: "network", Name: spec.Name})
		case existing.ConfigHash != spec.Identity.ConfigHash:
			removeNetworks = append(removeNetworks, Action{Verb: "remove", Type: "network", Name: spec.Name, ID: existing.ID})
			createNetworks = append(createNetworks, Action{Verb: "create", Type: "network", Name: spec.Name})
		}
	}
	for _, spec := range desired.Volumes {
		wanted["volume/"+spec.Name] = true
		if observed.find("volume", spec.Name) == nil {
			createVolumes = append(createVolumes, Action{Verb: "create", Type: "volume", Name: spec.Name})
		}
	}
	for _, spec := range desired.Containers {
		wanted["container/"+spec.Name] = true
		existing := observed.find("container", spec.Name)
		switch {
		case existing == nil:
			removed[spec.Name] = true
			createContainers = append(createContainers, Action{Verb: "create", Type: "container", Name: spec.Name})
		case existing.ConfigHash != spec.Identity.ConfigHash || recreate || existing.State == "created":
			// a container that was created and never started was cut short before its volumes were handed over
			removed[spec.Name] = true
			removeContainers = append(removeContainers, Action{Verb: "remove", Type: "container", Name: spec.Name, ID: existing.ID})
			createContainers = append(createContainers, Action{Verb: "create", Type: "container", Name: spec.Name})
		case existing.State != "running":
			starts = append(starts, Action{Verb: "start", Type: "container", Name: spec.Name, ID: existing.ID})
		}
	}

	for _, r := range observed.Resources {
		if wanted[r.Type+"/"+r.Name] || r.Type == "volume" {
			continue
		}
		if r.Type == "container" {
			removed[r.Name] = true
			removeContainers = append(removeContainers, Action{Verb: "remove", Type: "container", Name: r.Name, ID: r.ID})
			continue
		}
		// A sidecar may still be attached to a network that goes away: detach it first.
		for _, c := range observed.Resources {
			if c.Type == "container" && !removed[c.Name] && containsString(c.Networks, r.Name) {
				disconnects = append(disconnects, Action{Verb: "disconnect", Type: "network", Name: r.Name, ID: r.ID, Peer: c.Name})
			}
		}
		removeNetworks = append(removeNetworks, Action{Verb: "remove", Type: "network", Name: r.Name, ID: r.ID})
	}

	for _, attachment := range desired.Attachments {
		existing := observed.find("container", attachment.Container)
		for _, network := range attachment.Networks {
			if removed[attachment.Container] || existing == nil || !containsString(existing.Networks, network) {
				connects = append(connects, Action{Verb: "connect", Type: "network", Name: network, Peer: attachment.Container, Alias: attachment.Alias})
			}
		}
	}

	var plan []Action
	for _, group := range [][]Action{removeContainers, disconnects, removeNetworks, createNetworks, createVolumes, createContainers, starts, connects} {
		plan = append(plan, group...)
	}
	return plan
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
