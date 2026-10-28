// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"crypto/rand"
	"encoding/hex"
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
	"github.com/egzo-ai/egzo/internal/stack"
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
	if err := keepSecretKey(dir, cmd); err != nil {
		return err
	}

	template := strings.ReplaceAll(harness, "-code", "")
	fmt.Fprintf(cmd.OutOrStdout(), "next: edit the workspace url, check it with `egzo project check --resolved`, then `egzo project up` and `egzo agent spawn %s --attach`\n", template)
	if harness == "claude-code" {
		fmt.Fprintln(cmd.OutOrStdout(), "then sign in with your Claude subscription where Claude Code asks (/login): the proxy keeps the sign-in, and every agent after the first starts signed in")
	}
	return nil
}

// keepSecretKey makes the project's secret key and adds it to the project's .env, which git is then told to ignore. It
// never replaces a key: one in the environment or already in .env stays. A .env that git tracks, or that cannot be
// read, gets nothing: init prints the command that makes the key instead.
func keepSecretKey(dir string, cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	keep := "keep it: another key cannot open what egzo sealed with it"
	manual := func(why string) error {
		fmt.Fprintf(out, "%s: make the project's secret key, and %s\n  %s\n", why, keep, config.SecretKeyCommand("env", scaffoldSecretKey))
		return nil
	}
	if _, ok := os.LookupEnv(scaffoldSecretKey); ok {
		fmt.Fprintf(out, "the project's secret key is %s from your environment: %s\n", scaffoldSecretKey, keep)
		return nil
	}
	repo := isGitRepository(dir)
	if repo && gitTracks(dir, stack.DotenvFile) {
		return manual(stack.DotenvFile + " is tracked by git, so init writes no secret to it")
	}
	vars, err := stack.ReadDotenv(dir)
	if err != nil {
		return manual(err.Error())
	}
	if vars[scaffoldSecretKey] != "" {
		fmt.Fprintf(out, "the project's secret key is %s in %s: %s\n", scaffoldSecretKey, stack.DotenvFile, keep)
	} else {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		line := fmt.Sprintf("# egzo's project secret key. Keep it: another key cannot open what egzo sealed with it.\n%s=%s\n", scaffoldSecretKey, hex.EncodeToString(key))
		if err := appendLine(filepath.Join(dir, stack.DotenvFile), line, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "made the project's secret key, in %s: %s\n", stack.DotenvFile, keep)
	}
	if repo && !gitIgnores(dir, stack.DotenvFile) {
		if err := appendLine(filepath.Join(dir, ".gitignore"), stack.DotenvFile+"\n", 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "added %s to .gitignore\n", stack.DotenvFile)
	}
	return nil
}

// appendLine adds text at the end of a file, made with mode when it does not exist, on a line of its own.
func appendLine(path, text string, mode os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		text = "\n" + text
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, mode)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(text); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func isGitRepository(dir string) bool { return gitSucceeds(dir, "rev-parse", "--git-dir") }

func gitTracks(dir, name string) bool {
	return gitSucceeds(dir, "ls-files", "--error-unmatch", "--", name)
}

// gitIgnores asks git itself, so a global excludes file or .git/info/exclude counts.
func gitIgnores(dir, name string) bool { return gitSucceeds(dir, "check-ignore", "-q", "--", name) }

func gitSucceeds(dir string, args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// scaffoldEgress is the default profile the scaffold gives each integrated harness: the hosts it needs without a
// credential, and its provider. Claude Code signs in with a Claude subscription (the `claude` service, no vault secret);
// OpenCode cannot, and takes an API key.
var scaffoldEgress = map[string]string{
	"claude-code": `    services:
      claude: { credential: { method: oauth } }   # a Claude subscription: sign in from Claude Code (/login)
`,
	"opencode": `    allow: [models.opencode.ai]
    services:
      anthropic: main/ANTHROPIC_API_KEY
`,
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
	if profile, known := scaffoldEgress[harness]; known {
		fmt.Fprintf(&b, `egress:
  default:                      # everything not allowed here is denied
%s
`, profile)
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
