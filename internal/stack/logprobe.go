// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"

	"github.com/egzo-ai/egzo/internal/engine"
)

// egzo uses whatever logging the engine is set up with, and it reads container logs back for several jobs: the output of
// the git jobs, `egzo logs`, and the agents' status, which the hub follows in the control sidecar's log. LogProbe finds
// out, by running a container that prints to both streams, whether the engine's default can do that.

// LogProbe is what the engine's default logging did with a throwaway container.
type LogProbe struct {
	// Driver and Options are what the engine gave the container (the effective configuration, the daemon's own
	// options included).
	Driver  string
	Options map[string]string
	// Readable is whether the log could be read back at all. When it could not, ReadError says why.
	Readable  bool
	ReadError string
	// Separated is whether standard output and standard error came back apart.
	Separated bool
	// Followed is whether a line written while we were already following arrived while the container was still
	// running, which is what a client that follows needs.
	Followed bool
}

// probeScript writes a line to each stream, waits, and writes a last line: the wait is what shows a log is followed live.
const probeScript = "echo egzo-probe-out; echo egzo-probe-err >&2; sleep 2; echo egzo-probe-late"

// stamped passes writes through to a buffer and notes when the text first contained a marker.
type stamped struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marks  map[string]time.Time
	needle []string
}

func newStamped(needles ...string) *stamped {
	return &stamped{marks: map[string]time.Time{}, needle: needles}
}

func (s *stamped) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Write(p)
	now := time.Now()
	for _, n := range s.needle {
		if _, seen := s.marks[n]; !seen && strings.Contains(s.buf.String(), n) {
			s.marks[n] = now
		}
	}
	return len(p), nil
}

func (s *stamped) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *stamped) at(needle string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.marks[needle]
	return t, ok
}

// ProbeLogs runs a short container from image under the engine's default logging and reads its log back, following it. An error
// means the probe itself could not run (no such image, no engine); a log that cannot be read is a result, not an error.
func ProbeLogs(ctx context.Context, c *engine.Client, image string) (LogProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	identity := engine.Identity{Project: "egzo-system-check", Service: "logprobe", Kind: "prep"}
	config := &container.Config{Image: image, Cmd: strslice.StrSlice{"sh", "-c", probeScript}, Labels: identity.Labels()}
	// No LogConfig: it is the engine's default that is being tried.
	host := &container.HostConfig{CapDrop: strslice.StrSlice{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, NetworkMode: "none"}
	created, err := c.API.ContainerCreate(ctx, config, host, nil, nil, "")
	if err != nil {
		return LogProbe{}, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		c.API.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
	}()

	var probe LogProbe
	if inspected, err := c.API.ContainerInspect(ctx, created.ID); err == nil && inspected.HostConfig != nil {
		probe.Driver, probe.Options = inspected.HostConfig.LogConfig.Type, inspected.HostConfig.LogConfig.Config
	}
	if err := c.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return probe, err
	}

	reader, err := c.API.ContainerLogs(ctx, created.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		probe.ReadError = firstLineOf(err.Error())
		return probe, nil
	}
	defer reader.Close()
	out, errs := newStamped("egzo-probe-out", "egzo-probe-late"), newStamped("egzo-probe-err")
	if _, copyErr := engine.Demux(out, errs, reader); copyErr != nil && copyErr != io.EOF && ctx.Err() == nil {
		probe.ReadError = firstLineOf(copyErr.Error())
	}
	probe.Readable = strings.Contains(out.text(), "egzo-probe-out") || strings.Contains(errs.text(), "egzo-probe-err")
	if !probe.Readable && probe.ReadError == "" {
		probe.ReadError = "the container's output did not come back"
	}
	probe.Separated = strings.Contains(out.text(), "egzo-probe-out") && !strings.Contains(out.text(), "egzo-probe-err") &&
		strings.Contains(errs.text(), "egzo-probe-err")
	first, ok1 := out.at("egzo-probe-out")
	late, ok2 := out.at("egzo-probe-late")
	probe.Followed = ok1 && ok2 && late.Sub(first) >= time.Second
	return probe, nil
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

// String is for error messages.
func (p LogProbe) String() string {
	return fmt.Sprintf("driver %q options %v readable %v separated %v followed %v", p.Driver, p.Options, p.Readable, p.Separated, p.Followed)
}
