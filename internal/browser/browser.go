// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package browser configures the Playwright MCP server (`playwright-mcp`) of an agent container from the
// agent's environment, and runs it. It is the `egzo agent browser` role: the harness is told to start that
// instead of `playwright-mcp`, because Chromium cannot be given the proxy's credentials so that everything it sends
// carries them (it ignores `user:pass@` in a proxy URL, `--proxy-server` has no credentials, and the ones Playwright
// supplies are only for pages). The role runs a forwarder that adds them (forwarder.go), and gives Chromium that.
package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// Program is the Playwright MCP server the image installs.
const Program = "playwright-mcp"

// bypass keeps what is local to the agent (its dev server) off the proxy, which would refuse it.
const bypass = "localhost,127.0.0.1"

type proxySettings struct {
	Server string `json:"server"`
	Bypass string `json:"bypass"`
}

type launchOptions struct {
	Headless        bool           `json:"headless"`
	Channel         string         `json:"channel"`
	ChromiumSandbox bool           `json:"chromiumSandbox"`
	Args            []string       `json:"args"`
	Proxy           *proxySettings `json:"proxy,omitempty"`
}

type browserSettings struct {
	BrowserName   string        `json:"browserName"`
	Isolated      bool          `json:"isolated"`
	LaunchOptions launchOptions `json:"launchOptions"`
}

// config is the part of Playwright MCP's configuration file egzo writes (`@playwright/mcp`'s config.d.ts).
type config struct {
	Browser browserSettings `json:"browser"`
	// OutputDir is where it puts automatically named files (snapshots, screenshots). Left alone it is a
	// `.playwright-mcp` directory in the working directory, which is the agent's workspace.
	OutputDir string `json:"outputDir"`
}

// Config returns the configuration file for the proxy at proxyServer (the forwarder's URL, without credentials; empty for
// none) and the directory for the server's output.
func Config(proxyServer, outputDir string) ([]byte, error) {
	settings := config{
		Browser: browserSettings{
			BrowserName: "chromium",
			Isolated:    true,
			LaunchOptions: launchOptions{
				Headless: true,
				// "chromium" is the Chromium `playwright-mcp install-browser chrome-for-testing` installed (the
				// default channel for a headless launch is the headless shell, which the image does not carry).
				Channel:         "chromium",
				ChromiumSandbox: false,                               // the container is the boundary, and an unprivileged uid has no user namespaces
				Args:            []string{"--disable-dev-shm-usage"}, // /dev/shm is 64 MB
			},
		},
		OutputDir: outputDir,
	}
	if proxyServer != "" {
		settings.Browser.LaunchOptions.Proxy = &proxySettings{Server: proxyServer, Bypass: bypass}
	}
	return json.MarshalIndent(settings, "", "  ")
}

// Run starts the forwarder to the proxy of HTTPS_PROXY, writes the configuration into a private temporary directory, runs
// `playwright-mcp --config` on it with the environment given (its proxy the forwarder too), and removes the directory and
// closes the forwarder when the server exits. Signals go to the server, and its
// exit code is returned (128+n when a signal ended it).
func Run(environ []string, stdin, stdout, stderr *os.File) (int, error) {
	return run(Program, environ, stdin, stdout, stderr)
}

func run(program string, environ []string, stdin, stdout, stderr *os.File) (int, error) {
	dir, err := os.MkdirTemp("", "egzo-browser-")
	if err != nil {
		return 0, fmt.Errorf("make a directory for the browser's configuration: %w", err)
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		return 0, err
	}
	proxyURL := lookup(environ, "HTTPS_PROXY")
	if proxyURL == "" {
		proxyURL = lookup(environ, "https_proxy")
	}
	proxyServer := ""
	if proxyURL != "" {
		up, err := upstreamFrom(proxyURL)
		if err != nil {
			return 0, err
		}
		f, err := listenForwarder(up)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		go f.serve()
		proxyServer = f.URL()
	}
	data, err := Config(proxyServer, output)
	if err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return 0, fmt.Errorf("write the browser's configuration: %w", err)
	}

	cmd := exec.Command(program, "--config", path)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = childEnvironment(environ, proxyServer), stdin, stdout, stderr
	// Signals are caught before the server starts, so none is lost between starting it and forwarding.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("cannot run %s: %w", program, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}

// browsersPath is where the images install Chromium (PLAYWRIGHT_BROWSERS_PATH in their Dockerfiles).
var browsersPath = "/opt/ms-playwright"

// childEnvironment is the server's environment: the one it was given, with the browsers' location added when the
// harness did not pass it on (a harness may hand an MCP server a reduced environment), and the proxy variables pointed at the
// forwarder, so the credentials never reach the server and what it fetches itself goes out as the agent too.
func childEnvironment(environ []string, proxyServer string) []string {
	var out []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy":
			if proxyServer != "" {
				entry = name + "=" + proxyServer
			}
			out = append(out, entry)
		default:
			out = append(out, entry)
		}
	}
	if lookup(out, "PLAYWRIGHT_BROWSERS_PATH") == "" {
		if info, err := os.Stat(browsersPath); err == nil && info.IsDir() {
			out = append(out, "PLAYWRIGHT_BROWSERS_PATH="+browsersPath)
		}
	}
	return out
}

// lookup finds a variable in an environment list (the last one wins, as in a process's environment).
func lookup(environ []string, name string) string {
	value := ""
	for _, entry := range environ {
		if rest, ok := strings.CutPrefix(entry, name+"="); ok {
			value = rest
		}
	}
	return value
}
