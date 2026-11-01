// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exposing is a project of one agent, web, that declares an http port, a tcp port and a udp port. Local ports are on, with room for two.
func exposing(t *testing.T) *server {
	t.Helper()
	s, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.localMax = 2
	register(t, s, "web")
	declare(t, s, "web")
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

func declare(t *testing.T, s *server, agent string) {
	t.Helper()
	declared := map[string]Port{
		"app":   {Port: 3000, Protocol: ProtocolHTTP, Local: "http://box.lan:20001/"},
		"db":    {Port: 5432, Protocol: ProtocolTCP, Local: "box.lan:20002"},
		"voice": {Port: 5432, Protocol: ProtocolUDP, Local: "box.lan:20003"},
	}
	body, _ := json.Marshal(declared)
	w := newRecorder()
	s.handler().ServeHTTP(w, mustRequest("PUT", "/v1/agents/"+agent+"/ports", bytes.NewReader(body)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func hubTemplate(t *testing.T, s *server, template string) {
	t.Helper()
	w := newRecorder()
	s.handler().ServeHTTP(w, mustRequest("PUT", "/v1/preview-template", strings.NewReader(template)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func refusedWith(t *testing.T, err error, words ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want a refusal with %q", words)
	}
	for _, word := range words {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("%q does not say %q", err, word)
		}
	}
}

func TestAPreviewGetsItsIDBackAndARegistrationHasItsOwn(t *testing.T) {
	s := exposing(t)
	hubTemplate(t, s, "https://%s-p1.example.net/")
	first, err := s.expose("web", "app", WhereHub)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z2-7]{16}$`).MatchString(first.ID) || first.Address != "https://"+first.ID+"-p1.example.net/" {
		t.Fatalf("%+v", first)
	}
	if err := s.unexpose("web", "app"); err != nil {
		t.Fatal(err)
	}
	again, err := s.expose("web", "app", WhereHub)
	if err != nil || again.ID != first.ID {
		t.Errorf("%+v, %v", again, err)
	}
	gone := newRecorder()
	s.handler().ServeHTTP(gone, mustRequest("DELETE", "/v1/agents/web", nil))
	register(t, s, "web")
	declare(t, s, "web")
	next, err := s.expose("web", "app", WhereHub)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == first.ID {
		t.Error("a new instance of the name inherited the address")
	}
}

func TestALocalPortAnswersTheAddressTheStackGaveAndNeedsNoHub(t *testing.T) {
	s := exposing(t)
	s.localMax = 3
	for name, want := range map[string]string{"app": "http://box.lan:20001/", "db": "box.lan:20002", "voice": "box.lan:20003"} {
		got, err := s.expose("web", name, WhereLocal)
		if err != nil || got.Address != want || got.Where != WhereLocal || got.ID != "" {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	if got, _ := s.expose("web", "voice", WhereLocal); got.Protocol != ProtocolUDP || got.Port != 5432 {
		t.Errorf("%+v", got)
	}
}

func TestExposeIsRefusedWhenItCouldNotWork(t *testing.T) {
	s := exposing(t)
	_, err := s.expose("web", "nope", WhereLocal)
	refusedWith(t, err, "app", "db", "voice")
	_, err = s.expose("web", "app", "")
	refusedWith(t, err, "where")
	_, err = s.expose("web", "app", "everywhere")
	refusedWith(t, err, "where")
	_, err = s.expose("web", "app", WhereHub)
	refusedWith(t, err, "no hub")
	hubTemplate(t, s, "https://%s.example.net/")
	_, err = s.expose("web", "db", WhereHub)
	refusedWith(t, err, "only http", "local")
	_, err = s.expose("web", "voice", WhereHub)
	refusedWith(t, err, "only http")
	register(t, s, "bare") // a registered agent that declared nothing
	_, err = s.expose("bare", "app", WhereLocal)
	refusedWith(t, err, "declares no ports")
	s.localMax = 0
	_, err = s.expose("web", "app", WhereLocal)
	refusedWith(t, err, "local ports are off")
}

func TestAPortIsOpenForOneAudienceAtATime(t *testing.T) {
	s := exposing(t)
	hubTemplate(t, s, "https://%s.example.net/")
	first, err := s.expose("web", "app", WhereHub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.expose("web", "app", WhereLocal)
	refusedWith(t, err, "open for hub", "unexpose")
	if again, err := s.expose("web", "app", WhereHub); err != nil || again != first {
		t.Errorf("the same call again: %+v, %v", again, err)
	}
	if err := s.unexpose("web", "app"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.expose("web", "app", WhereLocal); err != nil || got.Where != WhereLocal {
		t.Errorf("%+v, %v", got, err)
	}
	_, err = s.expose("web", "app", WhereHub)
	refusedWith(t, err, "open for local")
}

func TestExposingTheSameThingAgainWritesNothing(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	wake := s.events.waker()
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Error("an agent that repeats itself woke the status log")
	default:
	}
}

func TestLocalPortsOpenInTheWholeProjectAreBounded(t *testing.T) {
	s := exposing(t)
	register(t, s, "other")
	declare(t, s, "other")
	hubTemplate(t, s, "https://%s.example.net/")
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expose("other", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	_, err := s.expose("web", "voice", WhereLocal)
	refusedWith(t, err, "unexpose one first")
	_, err = s.expose("other", "app", WhereLocal)
	refusedWith(t, err, "unexpose one first")
	if _, err := s.expose("web", "app", WhereHub); err != nil {
		t.Errorf("a preview counts for nothing: %v", err)
	}
	if err := s.unexpose("other", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expose("web", "voice", WhereLocal); err != nil {
		t.Errorf("room after an unexpose: %v", err)
	}
	s.localMax = 1 // lowered below what is open: nothing closes, nothing more opens
	if len(s.exposedBy("web", "")) != 3 {
		t.Errorf("%+v", s.exposedBy("web", ""))
	}
	if err := s.unexpose("web", "voice"); err != nil {
		t.Fatal(err)
	}
	_, err = s.expose("web", "voice", WhereLocal)
	refusedWith(t, err, "unexpose one first")
}

func TestUnexposeAnswersTheNoteOverMCPAndHTTP(t *testing.T) {
	want := "Active connections may remain active - close your server to end them."
	if UnexposeNote != want {
		t.Errorf("%q", UnexposeNote)
	}
	if got := unexposed(); !got.OK || got.Note != want {
		t.Errorf("%+v", got)
	}
}

func TestUnexposingWhatIsNotOpenIsWhatWasAsked(t *testing.T) {
	s := exposing(t)
	if err := s.unexpose("web", "app"); err != nil {
		t.Error(err)
	}
	if err := s.unexpose("web", "nothing-like-it"); err != nil {
		t.Error(err)
	}
}

func TestAnAgentThatIsNotRegisteredLosesItsExposuresAndPortsToo(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	// a spawn clears a name this way before it registers: nothing is retired, and nothing may be left for the new instance
	gone := newRecorder()
	s.handler().ServeHTTP(gone, mustRequest("DELETE", "/v1/agents/web", nil))
	if gone.Code != http.StatusNoContent {
		t.Fatalf("%d %s", gone.Code, gone.Body)
	}
	if list, err := s.readOpened("web"); err != nil || len(list) != 0 {
		t.Errorf("left behind: %+v, %v", list, err)
	}
	if ports, err := s.readPorts("web"); err != nil || len(ports) != 0 {
		t.Errorf("ports left behind: %+v, %v", ports, err)
	}
}

func TestPortsAreOnlyTakenForARegisteredAgentAndAreChecked(t *testing.T) {
	s := exposing(t)
	put := func(agent, body string) int {
		w := newRecorder()
		s.handler().ServeHTTP(w, mustRequest("PUT", "/v1/agents/"+agent+"/ports", strings.NewReader(body)))
		return w.Code
	}
	if put("ghost", `{}`) != http.StatusNotFound {
		t.Error("an agent that is not registered declared ports")
	}
	for _, body := range []string{`{"A":{"port":80,"protocol":"http"}}`, `{"a":{"port":0,"protocol":"http"}}`, `{"a":{"port":80,"protocol":"ftp"}}`, `[`} {
		if put("web", body) != http.StatusBadRequest {
			t.Errorf("accepted %s", body)
		}
	}
	many := map[string]Port{}
	for i := range maxExposures + 1 {
		many["p"+strings.Repeat("a", i%20)+string(rune('a'+i/20))] = Port{Port: 1000 + i, Protocol: ProtocolTCP}
	}
	data, _ := json.Marshal(many)
	if len(many) != maxExposures+1 || put("web", string(data)) != http.StatusBadRequest {
		t.Errorf("%d ports accepted", len(many))
	}
	if ports, _ := s.readPorts("web"); len(ports) != 3 {
		t.Errorf("a refused list replaced the kept one: %+v", ports)
	}
}

func TestABootEndsWhatWasExposedAndThePreviewGetsItsIDBack(t *testing.T) {
	s := exposing(t)
	hubTemplate(t, s, "https://%s.example.net/")
	first, err := s.expose("web", "app", WhereHub)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.boot("web"); err != nil {
		t.Fatal(err)
	}
	if list := s.exposedBy("web", ""); len(list) != 0 {
		t.Errorf("left: %+v", list)
	}
	again, err := s.expose("web", "app", WhereHub)
	if err != nil || again.ID != first.ID {
		t.Errorf("%+v, %v", again, err)
	}
}

func TestABootWithNothingExposedSaysNothing(t *testing.T) {
	s := exposing(t)
	wake := s.events.waker()
	if err := s.boot("web"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Error("a boot with nothing to end woke the status log")
	default:
	}
}

func TestABootClearsAListThatCannotBeRead(t *testing.T) {
	s := exposing(t)
	if err := os.MkdirAll(filepath.Dir(s.exposuresFile("web")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.exposuresFile("web"), []byte("["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.boot("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Error(err)
	}
}

func TestAListThatCannotBeReadIsNotReplaced(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	damaged := []byte(`[{"name":"db"`)
	if err := os.WriteFile(s.exposuresFile("web"), damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expose("web", "app", WhereLocal); err == nil {
		t.Error("expose answered as if nothing was exposed")
	}
	if err := s.unexpose("web", "db"); err == nil {
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
	before, after := []AgentStatus{{Agent: "web"}}, []AgentStatus{{Agent: "web", Exposed: []Exposure{{Name: "db"}}}}
	if sameState(before, after) {
		t.Error("the status log would not say so")
	}
}

func TestTheExposedListOfTheAgentIsWhatTheRelayGates(t *testing.T) {
	s := exposing(t)
	if _, err := s.expose("web", "db", WhereLocal); err != nil {
		t.Fatal(err)
	}
	list := s.exposedBy("web", "")
	if len(list) != 1 || list[0].Name != "db" || list[0].Where != WhereLocal || list[0].Address != "box.lan:20002" {
		t.Errorf("%+v", list)
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

func TestAPreviewOpenWhenTheHubGoesIsRefusedAgainUntilThereIsOne(t *testing.T) {
	s := exposing(t)
	hubTemplate(t, s, "https://%s.example.net/")
	if _, err := s.expose("web", "app", WhereHub); err != nil {
		t.Fatal(err)
	}
	hubTemplate(t, s, "")
	_, err := s.expose("web", "app", WhereHub)
	refusedWith(t, err, "no hub")
	hubTemplate(t, s, "https://%s.other.net/")
	if got, err := s.expose("web", "app", WhereHub); err != nil || !strings.Contains(got.Address, "other.net") {
		t.Errorf("%+v, %v", got, err)
	}
}
