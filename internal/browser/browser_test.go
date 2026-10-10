// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decode(t *testing.T, proxyURL string) map[string]any {
	t.Helper()
	data, err := Config(proxyURL, "/tmp/out")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func launch(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	return config["browser"].(map[string]any)["launchOptions"].(map[string]any)
}

func TestTheConfigurationRunsAnIsolatedHeadlessChromiumWithoutTheSandbox(t *testing.T) {
	config := decode(t, "")
	browser := config["browser"].(map[string]any)
	options := launch(t, config)
	if browser["browserName"] != "chromium" || browser["isolated"] != true {
		t.Errorf("browser = %v", browser)
	}
	if options["headless"] != true || options["channel"] != "chromium" || options["chromiumSandbox"] != false {
		t.Errorf("launchOptions = %v", options)
	}
	if args := options["args"].([]any); len(args) != 1 || args[0] != "--disable-dev-shm-usage" {
		t.Errorf("args = %v", args)
	}
	if config["outputDir"] != "/tmp/out" {
		t.Errorf("outputDir = %v", config["outputDir"])
	}
}

func TestTheProxyCredentialsAreSeparateFieldsAndThePasswordIsDecoded(t *testing.T) {
	config := decode(t, "http://coder:s3cr%22t%20p%26ss%3Dw0rd%2B%2F%25@proxy:3128")
	proxy := launch(t, config)["proxy"].(map[string]any)
	want := map[string]any{
		"server": "http://proxy:3128", "username": "coder", "password": `s3cr"t p&ss=w0rd+/%`, "bypass": "localhost,127.0.0.1",
	}
	for key, value := range want {
		if proxy[key] != value {
			t.Errorf("proxy[%s] = %v, want %v", key, proxy[key], value)
		}
	}
	if strings.Contains(proxy["server"].(string), "@") || strings.Contains(proxy["server"].(string), "coder") {
		t.Errorf("the server field carries the credentials: %v", proxy["server"])
	}
}

func TestAnIPv6ProxyHostKeepsItsBrackets(t *testing.T) {
	for raw, server := range map[string]string{
		"http://a:b@[fd00::2]:3128": "http://[fd00::2]:3128",
		"http://a:b@[fd00::2]":      "http://[fd00::2]:3128",
		"http://a:b@proxy":          "http://proxy:3128",
	} {
		proxy := launch(t, decode(t, raw))["proxy"].(map[string]any)
		if proxy["server"] != server {
			t.Errorf("%s: server = %v, want %s", raw, proxy["server"], server)
		}
	}
}

func TestAProxyWithoutCredentialsHasNoUserOrPasswordFields(t *testing.T) {
	proxy := launch(t, decode(t, "http://proxy:3128"))["proxy"].(map[string]any)
	if _, ok := proxy["username"]; ok {
		t.Errorf("proxy = %v", proxy)
	}
	if _, ok := proxy["password"]; ok {
		t.Errorf("proxy = %v", proxy)
	}
}

func TestWithoutAProxyThereIsNoProxyBlock(t *testing.T) {
	if _, ok := launch(t, decode(t, ""))["proxy"]; ok {
		t.Error("a proxy block was written for no proxy")
	}
}

func TestABrokenProxyURLIsAnErrorThatDoesNotRepeatTheCredentials(t *testing.T) {
	for _, raw := range []string{"http://coder:hunter2@proxy:99999999999%zz", "http://coder:hunter2@:3128", "%%%hunter2"} {
		_, err := Config(raw, "/tmp/out")
		if err == nil {
			t.Errorf("%q was accepted", raw)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the error leaks the password: %v", err)
		}
	}
}

func TestRunWritesAPrivateConfigurationForTheServerAndRemovesItAfterwards(t *testing.T) {
	scratch := t.TempDir()
	seen := filepath.Join(scratch, "seen")
	program := filepath.Join(scratch, "fake-playwright-mcp")
	script := "#!/bin/sh\n" +
		`[ "$1" = "--config" ] && [ "$#" = 2 ] || exit 90` + "\n" +
		`echo "$2" > ` + seen + "\n" +
		`stat -c %a "$2" >> ` + seen + "\n" +
		`cat "$2" >> ` + seen + "\n" +
		"exit 7\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	null, _ := os.Open(os.DevNull)
	defer null.Close()

	code, err := run(program, []string{"HTTPS_PROXY=http://coder:tok%40en@proxy:3128"}, null, null, null)
	if err != nil || code != 7 {
		t.Fatalf("run = %d, %v; want the server's exit code", code, err)
	}
	data, _ := os.ReadFile(seen)
	lines := strings.SplitN(string(data), "\n", 3)
	path, mode := lines[0], lines[1]
	if mode != "600" {
		t.Errorf("the configuration had mode %s", mode)
	}
	if !strings.Contains(lines[2], `"password": "tok@en"`) || !strings.Contains(lines[2], `"outputDir": "`+filepath.Dir(path)) {
		t.Errorf("configuration = %s", lines[2])
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the temporary directory outlived the server: %v", err)
	}
}

func TestRunRunsWithoutAProxyAndSaysWhenTheServerIsMissing(t *testing.T) {
	null, _ := os.Open(os.DevNull)
	defer null.Close()
	if _, err := run("/nonexistent/playwright-mcp", nil, null, null, null); err == nil || !strings.Contains(err.Error(), "cannot run") {
		t.Errorf("err = %v", err)
	}
}

func TestRunReportsASignalThatEndedTheServer(t *testing.T) {
	scratch := t.TempDir()
	program := filepath.Join(scratch, "fake")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nkill -TERM $$\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	null, _ := os.Open(os.DevNull)
	defer null.Close()
	if code, err := run(program, nil, null, null, null); err != nil || code != 128+15 {
		t.Errorf("run = %d, %v; want 143", code, err)
	}
}
