package stack

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// planIndex finds an action in a plan by its description.
func planIndex(t *testing.T, plan []Action, description string) int {
	t.Helper()
	for i, a := range plan {
		if a.String() == description {
			return i
		}
	}
	t.Fatalf("%q is not in the plan %v", description, verbs(plan))
	return -1
}

func TestDependenciesOrderCreatesAfterTheirPrerequisites(t *testing.T) {
	desired := Desired{
		Networks: []NetworkSpec{net("p_agent", "n")},
		Volumes:  []VolumeSpec{{Name: "p_data"}},
		Containers: []ContainerSpec{{
			Name:    "p-agent-1",
			Network: "p_agent",
			Mounts: []MountSpec{
				{Source: "p_data", Target: "/workspace/data"},
				{Bind: true, Source: "/host/dir", Target: "/workspace/dir"},
			},
		}},
	}
	plan := BuildPlan(desired, Observed{}, false)
	deps := dependencies(desired, plan)

	container := planIndex(t, plan, "create container p-agent-1")
	got := slices.Clone(deps[container])
	slices.Sort(got)
	want := []int{planIndex(t, plan, "create network p_agent"), planIndex(t, plan, "create volume p_data")}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("container deps = %v, want %v (the bind mount needs no volume)", got, want)
	}
	if d := deps[planIndex(t, plan, "create network p_agent")]; len(d) != 0 {
		t.Errorf("network deps = %v, want none", d)
	}
}

func TestDependenciesRecreateWaitsForRemoval(t *testing.T) {
	desired := Desired{
		Networks:   []NetworkSpec{net("p_a", "new")},
		Containers: []ContainerSpec{{Name: "p-a-1", Network: "p_a", Identity: ctr("", "new").Identity}},
	}
	observed := Observed{Resources: []Resource{
		{Type: "network", Name: "p_a", ID: "n1", ConfigHash: "old"},
		{Type: "container", Name: "p-a-1", ID: "c1", ConfigHash: "old", State: "running"},
	}}
	plan := BuildPlan(desired, observed, false)
	deps := dependencies(desired, plan)

	for _, c := range []struct{ action, after string }{
		{"create container p-a-1", "remove container p-a-1"},
		{"create network p_a", "remove network p_a"},
		{"create container p-a-1", "create network p_a"},
		{"remove network p_a", "remove container p-a-1"},
	} {
		if !slices.Contains(deps[planIndex(t, plan, c.action)], planIndex(t, plan, c.after)) {
			t.Errorf("%q should wait for %q; plan %v deps %v", c.action, c.after, verbs(plan), deps)
		}
	}
}

func TestDependenciesConnectWaitsForNetworkAndPeer(t *testing.T) {
	desired := Desired{
		Attachments: []Attachment{{Container: "p-control-1", Alias: "control", Networks: []string{"p_a"}}},
		Networks:    []NetworkSpec{net("p_a", "n")},
		Containers:  []ContainerSpec{ctr("p-control-1", "c")},
	}
	observed := Observed{Resources: []Resource{
		{Type: "container", Name: "p-control-1", ID: "c1", ConfigHash: "c", State: "exited"},
	}}
	plan := BuildPlan(desired, observed, false)
	deps := dependencies(desired, plan)

	connect := deps[planIndex(t, plan, "connect network p_a to p-control-1")]
	for _, prerequisite := range []string{"create network p_a", "start container p-control-1"} {
		if !slices.Contains(connect, planIndex(t, plan, prerequisite)) {
			t.Errorf("connect should wait for %q; deps %v", prerequisite, connect)
		}
	}
}

func TestDependenciesIndependentActionsHaveNone(t *testing.T) {
	desired := Desired{Networks: []NetworkSpec{net("p_a", "n"), net("p_b", "n")}}
	plan := BuildPlan(desired, Observed{}, false)
	for i, d := range dependencies(desired, plan) {
		if len(d) != 0 {
			t.Errorf("action %v depends on %v", plan[i], d)
		}
	}
}

func TestActionKey(t *testing.T) {
	if got := actionKey("create", "network", "p_a"); got != "create network p_a" {
		t.Errorf("actionKey = %q", got)
	}
}

func chain(n int) ([]Action, [][]int) {
	plan := make([]Action, n)
	deps := make([][]int, n)
	for i := range plan {
		plan[i] = Action{Name: string(rune('a' + i))}
		if i > 0 {
			deps[i] = []int{i - 1}
		}
	}
	return plan, deps
}

func TestRunConcurrentlyRespectsDependencies(t *testing.T) {
	plan, deps := chain(5)
	var mu sync.Mutex
	var order []string
	err := runConcurrently(context.Background(), plan, deps, func(_ context.Context, a Action) error {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, a.Name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c", "d", "e"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestRunConcurrentlyRunsIndependentActionsTogether(t *testing.T) {
	plan := []Action{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	deps := make([][]int, len(plan))

	// Every action blocks until all three are running, which only works if they run concurrently.
	var started atomic.Int32
	allStarted := make(chan struct{})
	err := runConcurrently(context.Background(), plan, deps, func(ctx context.Context, _ Action) error {
		if started.Add(1) == int32(len(plan)) {
			close(allStarted)
		}
		select {
		case <-allStarted:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("actions did not run concurrently")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunConcurrentlyStopsAtTheFirstFailure(t *testing.T) {
	plan, deps := chain(4)
	boom := errors.New("boom")
	var ran []string
	err := runConcurrently(context.Background(), plan, deps, func(_ context.Context, a Action) error {
		ran = append(ran, a.Name)
		if a.Name == "b" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("ran %v, want only %v: dependents of a failure must not start", ran, want)
	}
}

func TestRunConcurrentlyCancelsRunningActionsOnFailure(t *testing.T) {
	plan := []Action{{Name: "slow"}, {Name: "fails"}}
	deps := make([][]int, 2)
	boom := errors.New("boom")
	err := runConcurrently(context.Background(), plan, deps, func(ctx context.Context, a Action) error {
		if a.Name == "fails" {
			return boom
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("never cancelled")
		}
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the first failure, not the cancellation it caused", err)
	}
}

func TestRunConcurrentlyHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, deps := chain(2)
	err := runConcurrently(ctx, plan, deps, func(context.Context, Action) error {
		t.Error("an action ran after the context was cancelled")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunConcurrentlyEmptyPlan(t *testing.T) {
	if err := runConcurrently(context.Background(), nil, nil, nil); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestLockedWriterSerializesWrites(t *testing.T) {
	var buf safeBuffer
	w := &lockedWriter{w: &buf}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Write([]byte("line\n"))
		}()
	}
	wg.Wait()
	if got := buf.Len(); got != 50*len("line\n") {
		t.Errorf("wrote %d bytes, want %d", got, 50*len("line\n"))
	}
}

// safeBuffer fails the race detector if lockedWriter ever lets two writes overlap.
type safeBuffer struct{ data []byte }

func (b *safeBuffer) Write(p []byte) (int, error) { b.data = append(b.data, p...); return len(p), nil }
func (b *safeBuffer) Len() int                    { return len(b.data) }
