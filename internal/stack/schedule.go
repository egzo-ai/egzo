package stack

import (
	"context"
	"io"
	"sync"
)

// dependencies returns, for each action of the plan, the indexes of the actions that must finish
// before it can start. Everything else is independent and runs concurrently: the engine spends
// most of its time waiting on its own disk, so serial execution wastes it.
func dependencies(desired Desired, plan []Action) [][]int {
	index := map[string]int{}
	for i, a := range plan {
		index[actionKey(a.Verb, a.Type, a.Name)+peerKey(a)] = i
	}
	var removals, disconnects []int
	for i, a := range plan {
		switch {
		case a.Verb == "remove" && a.Type == "container":
			removals = append(removals, i)
		case a.Verb == "disconnect":
			disconnects = append(disconnects, i)
		}
	}
	specs := map[string]ContainerSpec{}
	for _, spec := range desired.Containers {
		specs[spec.Name] = spec
	}

	deps := make([][]int, len(plan))
	after := func(i int, verb, kind, name string) {
		if j, ok := index[actionKey(verb, kind, name)]; ok {
			deps[i] = append(deps[i], j)
		}
	}
	afterConnect := func(i int, network, peer string) {
		if j, ok := index[actionKey("connect", "network", network)+"|"+peer]; ok {
			deps[i] = append(deps[i], j)
		}
	}
	for i, a := range plan {
		switch {
		case a.Verb == "create":
			after(i, "remove", a.Type, a.Name)
			if a.Type == "container" {
				spec := specs[a.Name]
				after(i, "create", "network", spec.Network)
				for _, m := range spec.Mounts {
					if !m.Bind {
						after(i, "create", "volume", m.Source)
					}
				}
				for _, dependency := range spec.StartAfter {
					after(i, "create", "container", dependency)
					after(i, "start", "container", dependency)
				}
				// An agent starts once the sidecars it depends on are healthy and reachable on its
				// network: its first hook and its first tool call must not find nobody there.
				if spec.Identity.Kind == kindAgent {
					for _, attachment := range desired.Attachments {
						if containsString(attachment.Networks, spec.Network) {
							after(i, "create", "container", attachment.Container)
							after(i, "start", "container", attachment.Container)
							afterConnect(i, spec.Network, attachment.Container)
						}
					}
				}
			}
		case a.Verb == "start" && a.Type == "container":
			for _, dependency := range specs[a.Name].StartAfter {
				after(i, "create", "container", dependency)
				after(i, "start", "container", dependency)
			}
		case a.Verb == "remove" && a.Type == "network":
			deps[i] = append(deps[i], removals...)
			deps[i] = append(deps[i], disconnects...)
		case a.Verb == "connect":
			after(i, "create", "network", a.Name)
			after(i, "remove", "network", a.Name)
			after(i, "create", "container", a.Peer)
			after(i, "start", "container", a.Peer)
		}
	}
	return deps
}

func actionKey(verb, kind, name string) string { return verb + " " + kind + " " + name }

// peerKey tells apart the actions that move different containers in or out of the same network.
func peerKey(a Action) string {
	if a.Peer == "" {
		return ""
	}
	return "|" + a.Peer
}

// runConcurrently runs fn for every action once its dependencies are done. The first failure
// cancels what has not started yet and is the error returned.
func runConcurrently(ctx context.Context, plan []Action, deps [][]int, fn func(context.Context, Action) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make([]chan struct{}, len(plan))
	for i := range done {
		done[i] = make(chan struct{})
	}
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	fail := func(err error) {
		once.Do(func() {
			first = err
			cancel()
		})
	}
	for i, action := range plan {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[i])
			for _, j := range deps[i] {
				select {
				case <-done[j]:
				case <-ctx.Done():
					return
				}
			}
			if ctx.Err() != nil {
				return
			}
			if err := fn(ctx, action); err != nil {
				fail(err)
			}
		}()
	}
	wg.Wait()
	if first == nil {
		return ctx.Err()
	}
	return first
}

// lockedWriter serializes writes from concurrent actions.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
