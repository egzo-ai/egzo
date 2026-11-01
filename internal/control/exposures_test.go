// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"bytes"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

func exposing(t *testing.T) *server {
	t.Helper()
	s, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	register(t, s, "web")
	return s
}

func register(t *testing.T, s *server, agent string) {
	t.Helper()
	w := newRecorder()
	s.handler().ServeHTTP(w, mustRequest("PUT", "/v1/agents/"+agent, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAPortGetsItsIDBackAndARegistrationHasItsOwn(t *testing.T) {
	s := exposing(t)
	first, err := s.expose("web", 3000, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.unexpose("web", 3000); err != nil {
		t.Fatal(err)
	}
	again, err := s.expose("web", 3000, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Errorf("%s, then %s", first.ID, again.ID)
	}
	other, err := s.expose("web", 3001, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Error("two ports share an id")
	}
	gone := newRecorder()
	s.handler().ServeHTTP(gone, mustRequest("DELETE", "/v1/agents/web", nil))
	register(t, s, "web")
	next, err := s.expose("web", 3000, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == first.ID {
		t.Error("a new instance of the name inherited the address")
	}
}

func TestAnExposureKeepsItsIDAndTheAnswerFollowsTheTemplate(t *testing.T) {
	s := exposing(t)
	first, err := s.expose("web", 3000, "shop", "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z2-7]{16}$`).MatchString(first.ID) || first.Protocol != ProtocolHTTP {
		t.Fatalf("%+v", first)
	}
	if first.Address != "" || first.Command != "egzo agent port-forward web 3000" {
		t.Errorf("without a template: %+v", first)
	}
	put := newRecorder()
	s.handler().ServeHTTP(put, mustRequest("PUT", "/v1/preview-template", strings.NewReader("https://%s-p1.example.net/")))
	if put.Code != http.StatusNoContent {
		t.Fatalf("%d %s", put.Code, put.Body)
	}
	again, err := s.expose("web", 3000, "shop", "http")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Address != "https://"+first.ID+"-p1.example.net/" || again.Command != "" {
		t.Errorf("with a template: %+v", again)
	}
	tcp, err := s.expose("web", 3000, "shop", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if tcp.ID != first.ID || tcp.Address != "" || tcp.Command == "" {
		t.Errorf("tcp is no preview: %+v", tcp)
	}
}

func TestExposingNothingNewWritesNothing(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", 3000, "", ""); err != nil {
		t.Fatal(err)
	}
	wake := s.events.waker()
	if _, err := s.expose("web", 3000, "", "http"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Error("an agent that repeats itself woke the status log")
	default:
	}
}

func TestAnAgentThatIsNotRegisteredLosesItsExposuresToo(t *testing.T) {
	s := exposing(t)
	if err := s.writeExposures("old", []Exposure{{Port: 3000}}); err != nil {
		t.Fatal(err)
	}
	// a spawn clears a name this way before it registers: nothing is retired, and nothing may be left for the new instance
	gone := newRecorder()
	s.handler().ServeHTTP(gone, mustRequest("DELETE", "/v1/agents/old", nil))
	if gone.Code != http.StatusNoContent {
		t.Fatalf("%d %s", gone.Code, gone.Body)
	}
	if list, err := s.readExposures("old"); err != nil || len(list) != 0 {
		t.Errorf("left behind: %+v, %v", list, err)
	}
}

func TestAListThatCannotBeReadIsNotReplaced(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", 3000, "", ""); err != nil {
		t.Fatal(err)
	}
	damaged := []byte(`[{"port":3000`)
	if err := os.WriteFile(s.exposuresFile("web"), damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expose("web", 3001, "", ""); err == nil {
		t.Error("expose answered as if nothing was exposed")
	}
	if err := s.unexpose("web", 3000); err == nil {
		t.Error("unexpose answered as if nothing was exposed")
	}
	if got, err := os.ReadFile(s.exposuresFile("web")); err != nil || !bytes.Equal(got, damaged) {
		t.Errorf("the list was replaced: %q, %v", got, err)
	}
	if list := s.exposedBy("web", ""); len(list) != 0 {
		t.Errorf("%+v", list)
	}
}

func TestAnExposureInTheStatusListIsSeenAsAChange(t *testing.T) {
	before, after := []AgentStatus{{Agent: "web"}}, []AgentStatus{{Agent: "web", Exposed: []Exposure{{Port: 3000}}}}
	if sameState(before, after) {
		t.Error("the status log would not say so")
	}
}

func TestPreviewTemplates(t *testing.T) {
	for template, want := range map[string]bool{
		"https://%s-p1.example.net/":       true,
		"http://%s.preview.localhost:8081": true,
		"https://example.net/":             false,
		"https://%s.%s.example.net/":       false,
		"https://%s.example.net/%d":        false,
		"ftp://%s.example.net/":            false,
		"https://u:p@%s.example.net/":      false,
		"%s":                               false,
		"https://%s." + strings.Repeat("a", maxPreviewTemplate) + "/": false,
	} {
		if got := validPreviewTemplate(template); got != want {
			t.Errorf("%q: %v", template, got)
		}
	}
}

func TestATemplateThatIsNoAddressIsRefusedAndEmptyClears(t *testing.T) {
	s := exposing(t)
	put := func(body string) int {
		w := newRecorder()
		s.handler().ServeHTTP(w, mustRequest("PUT", "/v1/preview-template", strings.NewReader(body)))
		return w.Code
	}
	if put("nonsense") != http.StatusBadRequest {
		t.Error("refused nothing")
	}
	if put("https://%s.example.net/") != http.StatusNoContent || s.previewTemplate() == "" {
		t.Fatal("not kept")
	}
	if put("") != http.StatusNoContent || s.previewTemplate() != "" {
		t.Error("not cleared")
	}
	if put("") != http.StatusNoContent {
		t.Error("clearing twice is the same as once")
	}
}
