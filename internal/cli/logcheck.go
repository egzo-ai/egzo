// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"context"
	"fmt"

	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
)

// egzo does not choose a log driver: containers get the engine's default, which is the sysadmin's choice. But egzo reads logs
// back (the output of git jobs, `egzo logs`, the agents' status that the hub follows in the control sidecar's log), so
// `egzo system check` looks at the engine's default and, when the egzo image is there to run, tries it.

// logVerdict is what is known of a driver without trying it.
type logVerdict int

const (
	logUnknown    logVerdict = iota // not a driver egzo knows: it may or may not work
	logReadable                     // the engine reads it back, and follows it
	logUnreadable                   // the engine cannot give the log back at all
	logViaCache                     // a remote driver: Docker reads a local cache of it, which a sysadmin can turn off
)

// classifyLogDriver says what is known of a log driver by its name alone. It is a static judgement and cannot be more than that:
// the sysadmin's other settings (a turned-off cache, a journal that cannot be read) change the outcome, which is what the probe is for.
func classifyLogDriver(driver string, podman bool) logVerdict {
	switch driver {
	case "json-file", "journald":
		return logReadable
	case "local":
		if podman {
			return logUnknown
		}
		return logReadable
	case "k8s-file":
		if podman {
			return logReadable
		}
		return logUnknown
	case "none":
		return logUnreadable
	case "passthrough", "passthrough-tty":
		if podman {
			return logUnreadable
		}
		return logUnknown
	case "syslog", "gelf", "fluentd", "awslogs", "splunk", "gcplogs", "etwlogs", "logentries":
		if podman {
			return logUnknown
		}
		return logViaCache
	}
	return logUnknown
}

// logFinding is one line of the check.
type logFinding struct{ Level, Text string }

// readableDrivers is what to suggest when the engine's default cannot be read.
func readableDrivers(podman bool) string {
	if podman {
		return "k8s-file or journald"
	}
	return "json-file, local or journald"
}

// logFindings turns what is known into the lines the check prints. probe is nil when the engine's default could not be tried, and
// notTried says why.
func logFindings(podman bool, staticDriver string, probe *stack.LogProbe, notTried string) []logFinding {
	if probe == nil {
		driver := staticDriver
		if driver == "" {
			return []logFinding{{"warn", "logs: cannot tell which log driver the engine uses by default, and egzo reads container logs, so the engine must be able to give them back; not tried: " + notTried}}
		}
		switch classifyLogDriver(driver, podman) {
		case logReadable:
			return []logFinding{{"ok", fmt.Sprintf("logs: %s (the engine's default) is a driver that can be read and followed; not tried: %s", driver, notTried)}}
		case logUnreadable:
			return []logFinding{{"fail", fmt.Sprintf("logs: the engine's default log driver is %s, which cannot be read back: egzo reads container logs (git jobs, egzo logs, the agents' status in the hub). Set the engine's default to %s", driver, readableDrivers(podman))}}
		case logViaCache:
			return []logFinding{{"warn", fmt.Sprintf("logs: the engine's default log driver is %s: Docker reads it only through a local cache, which a sysadmin can turn off, so egzo cannot tell whether it can be read; not tried: %s", driver, notTried)}}
		}
		return []logFinding{{"warn", fmt.Sprintf("logs: egzo does not know whether the engine's default log driver %s can be read and followed; not tried: %s", driver, notTried)}}
	}

	driver := probe.Driver
	if driver == "" {
		driver = staticDriver
	}
	if driver == "" {
		driver = "unknown"
	}
	var findings []logFinding
	switch {
	case !probe.Readable:
		findings = append(findings, logFinding{"fail", fmt.Sprintf("logs: the engine's default log driver %s cannot be read back (%s): egzo reads container logs (git jobs, egzo logs, the agents' status in the hub). Set the engine's default to %s", driver, probe.ReadError, readableDrivers(podman))})
	case !probe.Followed:
		findings = append(findings, logFinding{"warn", fmt.Sprintf("logs: %s (the engine's default) can be read, but a log that is being followed arrived only when the container ended: the hub would see the agents' status late", driver)})
	case !probe.Separated:
		findings = append(findings, logFinding{"warn", fmt.Sprintf("logs: %s (the engine's default) can be read and followed, but standard output and standard error come back mixed: egzo cannot tell the status lines from other output", driver)})
	default:
		findings = append(findings, logFinding{"ok", fmt.Sprintf("logs: %s (the engine's default) can be read and followed, standard output and standard error apart", driver)})
	}
	// Docker puts the daemon's own log options into the container it makes, so what the probe saw is what every container gets.
	if !podman && probe.Readable && driver == "json-file" && probe.Options["max-size"] == "" {
		findings = append(findings, logFinding{"warn", "logs: json-file with no size limit: a chatty agent can fill the disk. Set log-opts max-size and max-file in the engine's daemon.json (or use the local driver), then run egzo project up again"})
	}
	return findings
}

// checkLogs looks at the engine's default logging, and tries it when the egzo image is on the engine.
func checkLogs(ctx context.Context, c *engine.Client, report func(level, format string, a ...any)) {
	staticDriver := ""
	if info, err := c.API.Info(ctx); err == nil {
		staticDriver = info.LoggingDriver
	}
	image := imageRef()
	var probe *stack.LogProbe
	notTried := ""
	if _, err := c.API.ImageInspect(ctx, image); err != nil {
		notTried = image + " is not on the engine (egzo project up pulls it)"
	} else if result, err := stack.ProbeLogs(ctx, c, image); err != nil {
		notTried = "the probe container could not run: " + firstLine(err.Error())
	} else {
		probe = &result
	}
	for _, f := range logFindings(c.IsPodman(), staticDriver, probe, notTried) {
		report(f.Level, "%s", f.Text)
	}
}
