package cli

import (
	"errors"
	"fmt"
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

	if added, err := ignoreWorkspaceData(dir); err != nil {
		return err
	} else if added {
		fmt.Fprintln(cmd.OutOrStdout(), "added .egzo/ to .gitignore")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "next: edit the workspace url, then run `egzo config` to check it")
	return nil
}

// scaffoldHosts are the hosts without a credential that each integrated harness needs, as the
// scaffold's allow list.
var scaffoldHosts = map[string]string{
	"claude-code": "platform.claude.com",
	"opencode":    "models.opencode.ai",
}

func scaffold(harness, repoURL string) string {
	var b strings.Builder
	b.WriteString("# egzo.yaml: see DESIGN.md for the full schema\n")
	if hosts, known := scaffoldHosts[harness]; known {
		fmt.Fprintf(&b, `vaults:
  main:
    backend: env
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }

egress:
  default:                      # everything not allowed here is denied
    allow: [%s]
    services:
      anthropic: main/ANTHROPIC_API_KEY

`, hosts)
	} else {
		b.WriteString("# TODO: add vaults and an egress profile that lets the agent reach its provider\n\n")
	}
	fmt.Fprintf(&b, `workspaces:
  repo:
    git: { url: %s }

agents:
  %s:
    harness: %s
    workspaces: [repo]
`, repoURL, strings.ReplaceAll(harness, "-code", ""), harness)
	return b.String()
}

var scpURL = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+?)(?:\.git)?$`)

// originURL returns the https form of the directory's git origin, or a placeholder.
func originURL(dir string) string {
	const placeholder = "https://github.com/OWNER/REPO.git"
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return placeholder
	}
	url := strings.TrimSpace(string(out))
	switch {
	case strings.HasPrefix(url, "https://"):
		return url
	case strings.HasPrefix(url, "ssh://"):
		return placeholder
	}
	if m := scpURL.FindStringSubmatch(url); m != nil && !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, ".") {
		return fmt.Sprintf("https://%s/%s.git", m[1], m[2])
	}
	return placeholder
}

// ignoreWorkspaceData keeps egzo's per-project data out of the user's repository when the
// directory is a git repository.
func ignoreWorkspaceData(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false, nil
	}
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == ".egzo/" || strings.TrimSpace(line) == ".egzo" {
			return false, nil
		}
	}
	text := string(existing)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return true, os.WriteFile(path, []byte(text+".egzo/\n"), 0o644)
}

func contains(list []string, value string) bool {
	i := sort.SearchStrings(list, value)
	return i < len(list) && list[i] == value
}
