// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTheChildGetsTheBrowsersPathWhenTheHarnessDidNotPassItOn(t *testing.T) {
	old := browsersPath
	defer func() { browsersPath = old }()
	browsersPath = t.TempDir()

	reduced := childEnvironment([]string{"PATH=/usr/bin"}) // no HTTPS_PROXY either
	if got := lookup(reduced, "PLAYWRIGHT_BROWSERS_PATH"); got != browsersPath {
		t.Errorf("PLAYWRIGHT_BROWSERS_PATH = %q", got)
	}
	if got := lookup(reduced, "PATH"); got != "/usr/bin" {
		t.Errorf("PATH = %q", got)
	}
	if got := lookup(childEnvironment([]string{"PLAYWRIGHT_BROWSERS_PATH=/mine"}), "PLAYWRIGHT_BROWSERS_PATH"); got != "/mine" {
		t.Errorf("a value that was set is replaced: %q", got)
	}
	browsersPath = filepath.Join(browsersPath, "missing")
	if got := lookup(childEnvironment(nil), "PLAYWRIGHT_BROWSERS_PATH"); got != "" {
		t.Errorf("a directory that does not exist was announced: %q", got)
	}
	in := []string{"A=1"}
	childEnvironment(in)
	if len(in) != 1 {
		t.Error("the caller's environment was modified")
	}
}

func TestRunForwardsSIGTERMToTheServerRemovesItsDirectoryAndPropagatesTheExitCode(t *testing.T) {
	scratch := t.TempDir()
	started, got, seen := filepath.Join(scratch, "started"), filepath.Join(scratch, "got"), filepath.Join(scratch, "config")
	program := filepath.Join(scratch, "fake")
	script := "#!/bin/sh\n" +
		`echo "$2" > ` + seen + "\n" +
		`trap 'echo term > ` + got + `; exit 3' TERM` + "\n" +
		"touch " + started + "\n" +
		"while :; do sleep 0.05; done\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	null, _ := os.Open(os.DevNull)
	defer null.Close()

	type result struct {
		code int
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		code, err := run(program, nil, null, null, null)
		finished <- result{code, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(os.Getpid(), syscall.SIGTERM) // caught by run, which forwards it
	select {
	case r := <-finished:
		if r.err != nil || r.code != 3 {
			t.Errorf("run = %d, %v; want the server's exit code 3", r.code, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after SIGTERM")
	}
	if _, err := os.Stat(got); err != nil {
		t.Error("the server never received the SIGTERM")
	}
	data, _ := os.ReadFile(seen)
	if dir := filepath.Dir(strings.TrimSpace(string(data))); dir == "" || dir == "." {
		t.Errorf("config path = %q", data)
	} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the temporary directory outlived the server: %v", err)
	}
}
