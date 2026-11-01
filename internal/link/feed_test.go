// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package link

import (
	"errors"
	"testing"
	"time"
)

func TestAStatusLineOfTheControlSidecarIsRead(t *testing.T) {
	line := `{"type":"status","v":1,"time":"2026-10-15T06:17:37.092Z","project":"p","reason":"change","interval":30,` +
		`"agents":[{"agent":"coder-1","status":"refactoring","activity":"working","open":2,"waiting":true,"updated":"2026-10-15T06:17:36Z"}]}`
	got, ok, err := parseStatusLine([]byte(line))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Reason != "change" || got.Interval != 30*time.Second || got.Time.IsZero() || len(got.Agents) != 1 {
		t.Fatalf("line = %+v", got)
	}
	if a := got.Agents[0]; a.Agent != "coder-1" || a.Status != "refactoring" || a.Activity != "working" || a.Open != 2 || !a.Waiting || a.Updated.IsZero() {
		t.Errorf("agent = %+v", a)
	}
}

func TestAnEmptyListOfAgentsIsAListSoAReaderReplacesWhatItKnew(t *testing.T) {
	got, ok, _ := parseStatusLine([]byte(`{"type":"status","v":1,"reason":"start","interval":30,"agents":[]}`))
	if !ok || got.Agents == nil || len(got.Agents) != 0 {
		t.Errorf("line = %+v ok=%v", got, ok)
	}
	got, ok, _ = parseStatusLine([]byte(`{"type":"status","v":1,"reason":"start","interval":30}`))
	if !ok || got.Agents == nil {
		t.Errorf("a line with no agents field: %+v", got)
	}
}

func TestWhatIsNotAStatusLineIsIgnoredAndANewerFormatIsAnError(t *testing.T) {
	for _, text := range []string{"", "not json", `{"type":"other","v":1}`, `{"msg":"hello"}`, `{"type":"status","v":1`, `[1,2]`} {
		if _, ok, err := parseStatusLine([]byte(text)); ok || err != nil {
			t.Errorf("%q: ok=%v err=%v", text, ok, err)
		}
	}
	_, ok, err := parseStatusLine([]byte(`{"type":"status","v":2,"agents":[]}`))
	var version ErrStatusVersion
	if ok || !errors.As(err, &version) || version.Version != 2 {
		t.Errorf("a format this hub does not read: ok=%v err=%v", ok, err)
	}
}

func TestAContainersHealthIsReadFromItsListedStatus(t *testing.T) {
	for status, want := range map[string]string{
		"Up 5 minutes (healthy)": "healthy", "Up 5 seconds (health: starting)": "starting", "Up 2 hours (unhealthy)": "unhealthy",
		"Up 5 minutes": "", "Exited (0) 3 minutes ago": "", "": "",
	} {
		if got := healthOf(status); got != want {
			t.Errorf("healthOf(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestOnlyContainersThatAreNotRunningAreStoppedInAListing(t *testing.T) {
	if activityOf("exited") != "stopped" || activityOf("created") != "stopped" {
		t.Error("a container that is not running is stopped")
	}
	if activityOf("running") != "" {
		t.Error("a listing cannot say what a running agent is doing, and must not invent it")
	}
}

func TestAStatusLineCarriesTheExposures(t *testing.T) {
	line, ok, err := parseStatusLine([]byte(`{"type":"status","v":1,"agents":[{"agent":"web","exposed":[{"port":3000,"name":"shop","protocol":"http","id":"abc","address":"https://abc.example.net/"}]}]}`))
	if err != nil || !ok || len(line.Agents) != 1 {
		t.Fatalf("%+v %v %v", line, ok, err)
	}
	if got := line.Agents[0].Exposed; len(got) != 1 || got[0].Port != 3000 || got[0].Name != "shop" || got[0].Protocol != "http" || got[0].ID != "abc" {
		t.Errorf("%+v", got)
	}
}

func TestATemplateTheSidecarWouldRefuseIsNoTemplate(t *testing.T) {
	for template, want := range map[string]string{
		"":                       "",
		"https://%s.example.net": "https://%s.example.net",
		"https://example.net/%s": "",
		"nonsense":               "",
	} {
		if got := usableTemplate(template); got != want {
			t.Errorf("%q: %q", template, got)
		}
	}
}
