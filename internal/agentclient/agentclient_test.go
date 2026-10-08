// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package agentclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoSendsTheAgentsCredentialsAndTheBody(t *testing.T) {
	var gotUser, gotToken, gotBody, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotToken, _ = r.BasicAuth()
		data, _ := io.ReadAll(r.Body)
		gotBody, gotPath = string(data), r.URL.Path
		io.WriteString(w, "reply")
	}))
	defer server.Close()
	client := &Client{URL: server.URL + "/", Agent: "coder", Token: "secret", HTTP: server.Client()}
	reply, err := client.Do(context.Background(), "POST", "/v1/status", map[string]string{"text": "hi"})
	if err != nil || string(reply) != "reply" {
		t.Fatalf("reply %q, %v", reply, err)
	}
	if gotUser != "coder" || gotToken != "secret" || gotPath != "/v1/status" || !strings.Contains(gotBody, `"text":"hi"`) {
		t.Errorf("user %q token %q path %q body %q", gotUser, gotToken, gotPath, gotBody)
	}
}

func TestDoTurnsAnErrorStatusIntoAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) }))
	defer server.Close()
	client := &Client{URL: server.URL, Agent: "a", Token: "t", HTTP: server.Client()}
	if _, err := client.Do(context.Background(), "GET", "/x", nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v", err)
	}
}

func TestFromEnvNeedsAllThreeVariables(t *testing.T) {
	t.Setenv("EGZO_CONTROL_URL", "http://control:7777")
	t.Setenv("EGZO_AGENT", "coder")
	t.Setenv("EGZO_TOKEN", "")
	if _, err := FromEnv(); err == nil {
		t.Error("a missing token was accepted")
	}
	t.Setenv("EGZO_TOKEN", "t")
	client, err := FromEnv()
	if err != nil || client.Agent != "coder" || client.URL != "http://control:7777" {
		t.Errorf("client = %+v, %v", client, err)
	}
}

func TestDoReportsAReplyThatIsTooLargeInsteadOfTruncatingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", 1<<20+10))
	}))
	defer server.Close()
	client := &Client{URL: server.URL, Agent: "a", Token: "t", HTTP: server.Client()}
	if _, err := client.Do(context.Background(), "GET", "/x", nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v", err)
	}
}
