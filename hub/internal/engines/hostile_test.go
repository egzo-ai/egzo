package engines

// What the hub does with what a compromised or broken engine answers. hostileEngine is a Docker API on a local port that answers
// what each test tells it to, so a test can send what no real engine would: frames that claim gigabytes, listings without end,
// labels that are not egzo's. The hub must refuse such answers with an error, in bounded memory and time, and never pass them on.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/engine"
)

// hostileEngine serves the parts of the Docker API the hub reads. Each handler is keyed by the end of the path ("containers/json",
// "logs", "events"); what has none answers an empty list.
type hostileEngine struct {
	t        *testing.T
	handlers map[string]http.HandlerFunc
}

func newHostileEngine(t *testing.T) *hostileEngine {
	return &hostileEngine{t: t, handlers: map[string]http.HandlerFunc{}}
}

func (h *hostileEngine) on(suffix string, f http.HandlerFunc) *hostileEngine {
	h.handlers[suffix] = f
	return h
}

// dial starts the engine and connects a Docker to it as the hub does.
func (h *hostileEngine) dial() *Docker {
	h.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			fmt.Fprint(w, "OK")
			return
		}
		for suffix, f := range h.handlers {
			if strings.HasSuffix(r.URL.Path, suffix) {
				f(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			fmt.Fprint(w, `{"Volumes":[]}`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	h.t.Cleanup(server.Close)
	client, err := engine.Dial(context.Background(), engine.Target{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")})
	if err != nil {
		h.t.Fatal(err)
	}
	d := NewDocker(client)
	h.t.Cleanup(func() { d.Close() })
	return d
}

// containers answers a listing with these containers, each given as its labels.
func containers(labels ...map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list := make([]map[string]any, len(labels))
		for i, l := range labels {
			list[i] = map[string]any{"Id": fmt.Sprintf("%064d", i), "Names": []string{fmt.Sprintf("/c%d", i)}, "Labels": l, "State": "running", "Status": "Up"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	}
}

func sidecar(project, kind string) map[string]string {
	return map[string]string{engine.LabelProject: project, engine.LabelKind: kind}
}

// frame is the header of one frame of a multiplexed stream: standard output, claiming size bytes.
func frame(size uint32) []byte {
	header := []byte{1, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[4:], size)
	return header
}

// allocated is how many bytes f allocated, all goroutines together.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

const allocationBound = 64 << 20

func TestALogFrameThatClaimsAGigabyteIsRefusedWithoutAllocatingIt(t *testing.T) {
	d := newHostileEngine(t).
		on("containers/json", containers(sidecar("web", kindControl))).
		on("/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			_, _ = w.Write(frame(1 << 30))
			_, _ = w.Write([]byte("a few bytes, and then the engine hangs up"))
		}).dial()

	var err error
	n := allocated(func() { err = d.FollowStatus(context.Background(), "web", func(StatusLine) {}) })
	if n > allocationBound {
		t.Errorf("following the status of a hostile engine allocated %d MiB for one frame header (err %v): the frame size must be bounded before a buffer is made", n>>20, err)
	}
	if err == nil {
		t.Error("a stream that broke off is an error")
	}
}

func TestAContainerLogThatClaimsAGigabyteFrameIsRefusedWithoutAllocatingIt(t *testing.T) {
	d := newHostileEngine(t).
		on("containers/json", containers(map[string]string{engine.LabelProject: "web", engine.LabelKind: kindAgent, engine.LabelInstance: "coder-1"})).
		on("/logs", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(frame(1 << 30))
		}).dial()

	var err error
	n := allocated(func() { _, err = d.Logs(context.Background(), "web", "coder-1", 100) })
	if n > allocationBound {
		t.Errorf("reading the log of a hostile engine allocated %d MiB for one frame header (err %v): Logs reads at most 4 MiB, the frame header must be held to that too", n>>20, err)
	}
}

func TestAListingWithoutEndIsCutOffInBoundedMemory(t *testing.T) {
	d := newHostileEngine(t).
		on("containers/json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			item := []byte(`{"Id":"x","Labels":{"ai.egzo.project":"web","ai.egzo.kind":"agent","ai.egzo.instance":"` + strings.Repeat("a", 4000) + `"}},`)
			_, _ = w.Write([]byte("["))
			for r.Context().Err() == nil {
				if _, err := w.Write(item); err != nil {
					return
				}
			}
		}).dial()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var err error
	n := allocated(func() { _, err = d.Projects(ctx) })
	if err == nil {
		t.Fatal("a listing without end is an error")
	}
	if n > 4*allocationBound {
		t.Errorf("a listing without end allocated %d MiB before it was cut off by the deadline (%v): a listing larger than any real engine sends must be refused as it is read", n>>20, err)
	}
}

func TestProjectsAndAgentsWhoseNamesEgzoWouldNeverMakeAreNotListed(t *testing.T) {
	agent := func(project, instance string) map[string]string {
		return map[string]string{engine.LabelProject: project, engine.LabelKind: kindAgent, engine.LabelInstance: instance, engine.LabelService: "coder"}
	}
	junk := []string{"../../etc", "<img src=x onerror=alert(1)>", "UPPER", "a/b", "\xff\xfe", strings.Repeat("x", 1<<16), "-dash", "has space"}
	labels := []map[string]string{sidecar("web", kindControl), sidecar("web", kindProxy), agent("web", "coder-1")}
	for _, name := range junk {
		labels = append(labels, sidecar(name, kindControl), agent(name, "coder-1"), agent("web", name))
	}
	d := newHostileEngine(t).on("containers/json", containers(labels...)).dial()

	projects, err := d.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range projects {
		names = append(names, fmt.Sprintf("%.40q", p.Name))
		if p.Name != "web" {
			continue
		}
		for _, a := range p.Agents {
			if a.Name != "coder-1" {
				t.Errorf("agent %.40q of web is listed: egzo names an instance with config's identifier rule, and nothing else is one", a.Name)
			}
		}
	}
	if len(projects) != 1 {
		t.Errorf("projects = %s: only web has a name egzo would give a project", strings.Join(names, ", "))
	}
}
