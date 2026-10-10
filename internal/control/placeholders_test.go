// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func placeholderCall(t *testing.T, s *server, method, body string) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, httptest.NewRequest(method, "/placeholders", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s /placeholders = %d %s", method, rec.Code, rec.Body)
	}
	out := map[string]string{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlaceholdersAreMadeOnceAndKept(t *testing.T) {
	s, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := placeholderCall(t, s, "GET", ""); len(got) != 0 {
		t.Fatalf("GET made or held something: %v", got)
	}
	first := placeholderCall(t, s, "POST", `["main/x"]`)
	shape := regexp.MustCompile(`^egzo-ph-[0-9a-f]{32}$`)
	if !shape.MatchString(first["main/x"]) {
		t.Fatalf("placeholder %q has the wrong shape", first["main/x"])
	}
	both := placeholderCall(t, s, "POST", `["main/x","other"]`)
	if both["main/x"] != first["main/x"] || both["other"] == first["main/x"] || both["other"] == "" {
		t.Fatalf("placeholders = %v after %v", both, first)
	}
	again, _ := newServer(s.dir) // a restarted sidecar on the same volume
	if got := placeholderCall(t, again, "GET", ""); got["main/x"] != first["main/x"] {
		t.Errorf("the placeholder did not survive a restart: %v", got)
	}
}
