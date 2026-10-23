package engines

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

	"github.com/egzo-ai/egzo/internal/engine"
)

// ErrStreamEnded is returned when an engine's stream ended without being asked to: the container stopped, or the connection dropped.
var ErrStreamEnded = errors.New("the stream ended")

// statusTail is how many lines of the log are read when the stream opens. The newest status line is among them unless the
// sidecar said more than this on standard error since: the engines count the lines of both streams before they filter one.
const statusTail = "100"

// maxLogLine is the longest line read from a log.
const maxLogLine = 4 << 20

// statusLogLine is one line of the control sidecar's standard output (core: internal/control/statuslog.go).
type statusLogLine struct {
	Type     string `json:"type"`
	V        int    `json:"v"`
	Time     time.Time
	Reason   string
	Interval int
	Agents   []struct {
		Agent    string
		Status   string
		Activity string
		Open     int
		Waiting  bool
		Updated  time.Time
	}
}

// ErrStatusVersion is returned when the sidecar writes a status format this hub does not read.
type ErrStatusVersion struct{ Version int }

func (e ErrStatusVersion) Error() string {
	return fmt.Sprintf("the control sidecar writes status format %d, which this hub does not read: update the hub", e.Version)
}

// parseStatusLine reads one line of a control sidecar's standard output. ok is false for a line that is not a status line.
func parseStatusLine(line []byte) (StatusLine, bool, error) {
	var raw statusLogLine
	if err := json.Unmarshal(line, &raw); err != nil || raw.Type != "status" {
		return StatusLine{}, false, nil
	}
	if raw.V != 1 {
		return StatusLine{}, false, ErrStatusVersion{Version: raw.V}
	}
	out := StatusLine{Time: raw.Time, Reason: raw.Reason, Interval: time.Duration(raw.Interval) * time.Second, Agents: []AgentStatus{}}
	for _, a := range raw.Agents {
		out.Agents = append(out.Agents, AgentStatus{Agent: a.Agent, Status: a.Status, Activity: a.Activity, Open: a.Open, Waiting: a.Waiting, Updated: a.Updated})
	}
	return out, true, nil
}

// FollowStatus follows the standard output of the project's control sidecar through the engine's log API. Only standard output is
// asked for: the engines tell the two streams apart whatever the log driver, and the sidecar keeps its status there.
func (d *Docker) FollowStatus(ctx context.Context, project string, on func(StatusLine)) error {
	list, err := d.client.API.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"="+kindControl), filters.Arg("status", "running"))})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return ErrNoProject{Name: project}
	}
	return followLines(ctx, d, list[0].ID, container.LogsOptions{ShowStdout: true, Follow: true, Tail: statusTail}, func(raw []byte) error {
		line, ok, err := parseStatusLine(raw)
		if err != nil {
			return err
		}
		if ok {
			on(line)
		}
		return nil
	})
}

// FollowAudit follows the standard output of the project's proxy, where it writes its audit trail, one JSON event per line.
func (d *Docker) FollowAudit(ctx context.Context, project string, since time.Time, on func(AuditEvent)) error {
	list, err := d.client.API.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", engine.LabelProject+"="+project), filters.Arg("label", engine.LabelKind+"="+kindProxy), filters.Arg("status", "running"))})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return ErrNoProject{Name: project}
	}
	options := container.LogsOptions{ShowStdout: true, Follow: true, Tail: fmt.Sprint(AuditBacklog)}
	if !since.IsZero() {
		options.Tail, options.Since = "", since.Format(time.RFC3339Nano)
	}
	return followLines(ctx, d, list[0].ID, options, func(line []byte) error {
		var event AuditEvent
		if json.Unmarshal(line, &event) == nil && event.Conn != "" && !event.Time.IsZero() {
			on(event)
		}
		return nil
	})
}

// followLines follows the standard output of a container through the engine's log API and calls on for each line.
func followLines(ctx context.Context, d *Docker, id string, options container.LogsOptions, on func([]byte) error) error {
	reader, err := d.client.API.ContainerLogs(ctx, id, options)
	if err != nil {
		return err
	}
	// Closing the reader is how a follow ends when the context does.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			reader.Close()
		case <-stopped:
		}
	}()
	defer reader.Close()

	lines, writer := io.Pipe()
	go func() {
		_, copyErr := engine.Demux(writer, io.Discard, reader)
		writer.CloseWithError(copyErr)
	}()
	scanner := bufio.NewScanner(lines)
	scanner.Buffer(make([]byte, 64<<10), maxLogLine)
	for scanner.Scan() {
		if err := on(scanner.Bytes()); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return ErrStreamEnded
}

// watched are the container events that can change what the hub lists; the rest (exec, attach, resize) are noise, and the hub
// itself makes them all the time.
var watched = map[events.Action]bool{
	events.ActionCreate: true, events.ActionStart: true, events.ActionDie: true, events.ActionDestroy: true,
	events.ActionStop: true, events.ActionRestart: true, events.ActionRename: true, events.ActionKill: true,
}

// Watch follows the engine's event stream, for the containers that belong to an egzo project.
func (d *Docker) Watch(ctx context.Context, changed func()) error {
	messages, errs := d.client.API.Events(ctx, events.ListOptions{Filters: filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)), filters.Arg("label", engine.LabelProject))})
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil || errors.Is(err, io.EOF) {
				return ErrStreamEnded
			}
			return err
		case message, ok := <-messages:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return ErrStreamEnded
			}
			if watched[message.Action] || strings.HasPrefix(string(message.Action), "health_status") {
				changed()
			}
		}
	}
}
