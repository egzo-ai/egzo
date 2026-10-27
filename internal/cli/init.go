// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
)

func newInitCommand() *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold an egzo.yaml in the current directory",
		Long: "Scaffold a small egzo.yaml: one agent, one git workspace, and an egress profile that lets the agent\n" +
			"reach its model API. Nothing from the current directory is mounted into the agent unless you add it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			return initProject(dir, harness, cmd)
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "claude-code", "harness of the first agent")
	return cmd
}

func initProject(dir, harness string, cmd *cobra.Command) error {
	known := config.HarnessNames()
	if !contains(known, harness) {
		return fmt.Errorf("unknown harness %q (one of: %s)", harness, strings.Join(known, ", "))
	}
	path := filepath.Join(dir, config.FileName)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; init never overwrites it", config.FileName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.WriteFile(path, []byte(scaffold(harness, originURL(dir))), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", config.FileName)
	fmt.Fprintf(cmd.OutOrStdout(), "make the project's secret key, and keep it: another key cannot open what egzo sealed with it\n  %s\n",
		config.SecretKeyCommand("env", scaffoldSecretKey))

	template := strings.ReplaceAll(harness, "-code", "")
	fmt.Fprintf(cmd.OutOrStdout(), "next: edit the workspace url, check it with `egzo project check --resolved`, then `egzo project up` and `egzo agent spawn %s --attach`\n", template)
	return nil
}

// scaffoldHosts are the hosts without a credential that each integrated harness needs, as the
// scaffold's allow list.
var scaffoldHosts = map[string]string{
	"claude-code": "platform.claude.com",
	"opencode":    "models.opencode.ai",
}

// scaffoldSecretKey is the variable the scaffold reads the project's secret key from.
const scaffoldSecretKey = "EGZO_PROJECT_SECRET_KEY"

func scaffold(harness, repoURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `secret_key: main/%s     # seals what egzo keeps at rest

vaults:
  main:
    backend: env

`, scaffoldSecretKey)
	if hosts, known := scaffoldHosts[harness]; known {
		fmt.Fprintf(&b, `egress:
  default:                      # everything not allowed here is denied
    allow: [%s]
    services:
      anthropic: main/ANTHROPIC_API_KEY

`, hosts)
	} else {
		b.WriteString("# TODO: add an egress profile that lets the agent reach its provider\n\n")
	}
	image := ""
	if harness == "custom" {
		image = "\n    image: docker.io/library/alpine:3   # TODO: the image that runs your program"
	}
	fmt.Fprintf(&b, `workspaces:
  repo:
    git: { url: %q }

agents:
  %s:
    harness: %s%s
    workspaces: [repo]
`, repoURL, strings.ReplaceAll(harness, "-code", ""), harness, image)
	return b.String()
}

const placeholderURL = "https://github.com/OWNER/REPO.git"

var scpURL = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+?)(?:\.git)?$`)

// originURL returns the https form of the directory's git origin, or a placeholderURL.
func originURL(dir string) string {
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return placeholderURL
	}
	url := strings.TrimSpace(string(out))
	switch {
	case strings.HasPrefix(url, "https://"):
		return withoutCredentials(url)
	case strings.HasPrefix(url, "ssh://"):
		return placeholderURL
	}
	if m := scpURL.FindStringSubmatch(url); m != nil && !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, ".") {
		return fmt.Sprintf("https://%s/%s.git", m[1], m[2])
	}
	return placeholderURL
}

func contains(list []string, value string) bool {
	i := sort.SearchStrings(list, value)
	return i < len(list) && list[i] == value
}

// withoutCredentials drops the user name and password of a URL: a token in an origin URL must not be
// copied into a file that is committed.
func withoutCredentials(raw string) string {
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Host == "" {
		return placeholderURL
	}
	parsed.User = nil
	return parsed.String()
}
