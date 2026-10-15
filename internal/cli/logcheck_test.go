// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/stack"
)

func TestLogDriversAreClassifiedByWhatTheEngineCanDoWithThem(t *testing.T) {
	cases := []struct {
		driver string
		podman bool
		want   logVerdict
	}{
		{"json-file", false, logReadable}, {"local", false, logReadable}, {"journald", false, logReadable},
		{"none", false, logUnreadable},
		{"syslog", false, logViaCache}, {"gelf", false, logViaCache}, {"fluentd", false, logViaCache}, {"awslogs", false, logViaCache},
		{"splunk", false, logViaCache}, {"gcplogs", false, logViaCache}, {"etwlogs", false, logViaCache},
		{"some-plugin", false, logUnknown}, {"", false, logUnknown},
		// Podman has its own drivers; json-file is an alias there
		{"k8s-file", true, logReadable}, {"json-file", true, logReadable}, {"journald", true, logReadable},
		{"none", true, logUnreadable}, {"passthrough", true, logUnreadable}, {"passthrough-tty", true, logUnreadable},
		{"syslog", true, logUnknown}, {"local", true, logUnknown},
		// and a Docker-only name is not a Podman one, nor the other way round
		{"k8s-file", false, logUnknown}, {"passthrough", false, logUnknown},
	}
	for _, c := range cases {
		if got := classifyLogDriver(c.driver, c.podman); got != c.want {
			t.Errorf("classify(%q, podman=%v) = %v, want %v", c.driver, c.podman, got, c.want)
		}
	}
}

func level(findings []logFinding) string {
	var levels []string
	for _, f := range findings {
		levels = append(levels, f.Level)
	}
	return strings.Join(levels, ",")
}

func TestWithoutATryTheCheckJudgesTheDriverByName(t *testing.T) {
	cases := []struct {
		driver, want string
		podman       bool
	}{
		{"json-file", "ok", false}, {"journald", "ok", false}, {"k8s-file", "ok", true},
		{"none", "fail", false}, {"none", "fail", true}, {"passthrough", "fail", true},
		{"syslog", "warn", false}, {"some-plugin", "warn", false}, {"", "warn", true},
	}
	for _, c := range cases {
		got := logFindings(c.podman, c.driver, nil, "the image is not on the engine")
		if len(got) != 1 || got[0].Level != c.want {
			t.Errorf("driver %q podman=%v: %+v, want one %s", c.driver, c.podman, got, c.want)
			continue
		}
		// a name that settles it needs no try; every other answer says it was not tried
		if c.want != "fail" && !strings.Contains(got[0].Text, "not tried") {
			t.Errorf("driver %q podman=%v: %q does not say it was not tried", c.driver, c.podman, got[0].Text)
		}
		if !strings.HasPrefix(got[0].Text, "logs:") {
			t.Errorf("the line must begin with logs: %q", got[0].Text)
		}
	}
	if text := logFindings(false, "none", nil, "x")[0].Text; !strings.Contains(text, "json-file, local or journald") {
		t.Errorf("the fix for Docker names Docker's drivers: %q", text)
	}
	if text := logFindings(true, "none", nil, "x")[0].Text; !strings.Contains(text, "k8s-file or journald") {
		t.Errorf("the fix for Podman names Podman's drivers: %q", text)
	}
}

func TestATryDecidesWhatTheNameCannot(t *testing.T) {
	good := stack.LogProbe{Driver: "journald", Readable: true, Separated: true, Followed: true}
	if got := logFindings(false, "journald", &good, ""); level(got) != "ok" || !strings.Contains(got[0].Text, "read and followed") {
		t.Errorf("a good log: %+v", got)
	}
	// a remote driver that the name alone cannot vouch for, but that worked
	remote := stack.LogProbe{Driver: "syslog", Readable: true, Separated: true, Followed: true}
	if got := logFindings(false, "syslog", &remote, ""); level(got) != "ok" {
		t.Errorf("a remote driver that works: %+v", got)
	}
	cannot := stack.LogProbe{Driver: "none", ReadError: "configured logging driver does not support reading"}
	if got := logFindings(false, "none", &cannot, ""); level(got) != "fail" || !strings.Contains(got[0].Text, "does not support reading") {
		t.Errorf("a log that cannot be read: %+v", got)
	}
	late := stack.LogProbe{Driver: "local", Readable: true, Separated: true, Followed: false}
	if got := logFindings(false, "local", &late, ""); level(got) != "warn" || !strings.Contains(got[0].Text, "late") {
		t.Errorf("a log that is not followed live: %+v", got)
	}
	mixed := stack.LogProbe{Driver: "journald", Readable: true, Separated: false, Followed: true}
	if got := logFindings(false, "journald", &mixed, ""); level(got) != "warn" || !strings.Contains(got[0].Text, "mixed") {
		t.Errorf("streams that come back mixed: %+v", got)
	}
}

func TestADefaultWithNoSizeLimitIsAWarningOnlyWhereTheCheckCanSeeIt(t *testing.T) {
	unbounded := stack.LogProbe{Driver: "json-file", Options: map[string]string{}, Readable: true, Separated: true, Followed: true}
	if got := logFindings(false, "json-file", &unbounded, ""); level(got) != "ok,warn" || !strings.Contains(got[1].Text, "no size limit") {
		t.Errorf("Docker json-file with no max-size: %+v", got)
	}
	bounded := unbounded
	bounded.Options = map[string]string{"max-size": "10m", "max-file": "3"}
	if got := logFindings(false, "json-file", &bounded, ""); level(got) != "ok" {
		t.Errorf("a bounded default: %+v", got)
	}
	// Podman does not show a size limit in what it reports, so nothing is said about one
	if got := logFindings(true, "k8s-file", &stack.LogProbe{Driver: "k8s-file", Readable: true, Separated: true, Followed: true}, ""); level(got) != "ok" {
		t.Errorf("Podman: %+v", got)
	}
	// and without a try, the daemon's options are not known
	if got := logFindings(false, "json-file", nil, "x"); level(got) != "ok" {
		t.Errorf("not tried: %+v", got)
	}
}
