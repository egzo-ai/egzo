// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package browser

import (
	"encoding/base64"
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

func TestTheProxyIsTheForwarderWithoutCredentialsAndLocalhostBypassesIt(t *testing.T) {
	proxy := launch(t, decode(t, "http://127.0.0.1:4567"))["proxy"].(map[string]any)
	if len(proxy) != 2 || proxy["server"] != "http://127.0.0.1:4567" || proxy["bypass"] != "localhost,127.0.0.1" {
		t.Errorf("proxy = %v", proxy)
	}
}

func TestWithoutAProxyThereIsNoProxyBlock(t *testing.T) {
	if _, ok := launch(t, decode(t, ""))["proxy"]; ok {
		t.Error("a proxy block was written for no proxy")
	}
}

func TestTheUpstreamIsTheProxyOfHTTPSProxyWithItsCredentialsDecoded(t *testing.T) {
	up, err := upstreamFrom("http://coder:s3cr%22t%20p%26ss%3Dw0rd%2B%2F%25@proxy:3128")
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(`coder:s3cr"t p&ss=w0rd+/%`))
	if up.address != "proxy:3128" || up.authorization != want {
		t.Errorf("upstream = %+v", up)
	}
	for raw, address := range map[string]string{
		"http://a:b@[fd00::2]:3128": "[fd00::2]:3128",
		"http://a:b@[fd00::2]":      "[fd00::2]:3128",
		"http://a:b@proxy":          "proxy:3128",
	} {
		if up, _ := upstreamFrom(raw); up.address != address {
			t.Errorf("%s: address = %q, want %s", raw, up.address, address)
		}
	}
	if up, _ := upstreamFrom("http://proxy:3128"); up.authorization != "" {
		t.Errorf("without credentials: %+v", up)
	}
}

func TestABrokenProxyURLIsAnErrorThatDoesNotRepeatTheCredentials(t *testing.T) {
	for _, raw := range []string{"http://coder:hunter2@proxy:99999999999%zz", "http://coder:hunter2@:3128", "%%%hunter2", "https://coder:hunter2@proxy:3128"} {
		_, err := upstreamFrom(raw)
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
		`echo "$HTTPS_PROXY" >> ` + seen + "\n" +
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
	lines := strings.SplitN(string(data), "\n", 4)
	path, mode, proxy := lines[0], lines[1], lines[2]
	if mode != "600" {
		t.Errorf("the configuration had mode %s", mode)
	}
	// the server and Chromium are given the forwarder, never the credentials
	if !strings.HasPrefix(proxy, "http://127.0.0.1:") || !strings.Contains(lines[3], `"server": "`+proxy+`"`) || strings.Contains(string(data), "tok") {
		t.Errorf("HTTPS_PROXY %s, configuration = %s", proxy, lines[3])
	}
	if !strings.Contains(lines[3], `"outputDir": "`+filepath.Dir(path)) {
		t.Errorf("configuration = %s", lines[3])
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
