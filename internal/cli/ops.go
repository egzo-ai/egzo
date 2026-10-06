package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/stack"
)

// --- doctor ---------------------------------------------------------------------------------------

func newDoctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the engine, its isolation and this project",
		Long: "Check what egzo depends on: that the engine answers, whether it runs rootless, whether gVisor is\n" +
			"registered, and the project in this directory. Each line starts with ok, warn or fail; egzo exits\n" +
			"non-zero when any check fails.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			out := cmd.OutOrStdout()
			failed := false
			report := func(level, format string, a ...any) {
				if level == "fail" {
					failed = true
				}
				fmt.Fprintf(out, "%-4s %s\n", level, fmt.Sprintf(format, a...))
			}

			c, err := engine.Connect(ctx)
			if err != nil {
				report("fail", "engine: %s", firstLine(err.Error()))
			} else {
				defer c.Close()
				checkEngine(ctx, c, report)
			}
			checkProject(opts, report)
			if failed {
				return ExitError{Code: 1}
			}
			return nil
		},
	}
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func checkEngine(ctx context.Context, c *engine.Client, report func(level, format string, a ...any)) {
	name := "Docker"
	if c.Podman {
		name = "Podman"
	}
	version, err := c.API.ServerVersion(ctx)
	if err != nil {
		report("fail", "engine: %v", err)
		return
	}
	report("ok", "engine: %s %s answers at %s", name, version.Version, c.Host)

	info, err := c.API.Info(ctx)
	if err != nil {
		report("warn", "engine: cannot read its configuration: %v", err)
		return
	}
	rootless := false
	for _, option := range info.SecurityOptions {
		rootless = rootless || strings.Contains(option, "rootless")
	}
	if rootless {
		report("ok", "engine runs rootless: a compromised container is only you")
	} else {
		report("ok", "engine runs rootful: agents run as your user without capabilities, but a container escape is root; consider rootless or gVisor")
	}
	checkInternalNetworks(ctx, c, report)
	if _, ok := info.Runtimes["runsc"]; ok {
		report("ok", "gVisor (runsc) is registered: set runtime: runsc on an agent to sandbox it further")
	} else {
		report("warn", "gVisor (runsc) is not registered: agents run under %s, the engine's own runtime", info.DefaultRuntime)
	}
}

func checkProject(opts *options, report func(level, format string, a ...any)) {
	path, err := projectFile(opts)
	if err != nil {
		report("ok", "no %s here or in a parent directory: project checks skipped", config.FileName)
		return
	}
	dir := filepath.Dir(path)
	p, err := loadProject(opts)
	if err != nil {
		report("fail", "%s: %s", config.FileName, firstLine(err.Error()))
		return
	}
	report("ok", "%s is valid (project %s, %d template(s))", config.FileName, p.Resolved.Name, len(p.Resolved.Agents))
	for _, warning := range p.Warnings {
		report("warn", "%s", strings.TrimPrefix(warning, "warning: "))
	}
	for _, warning := range secretFileWarnings(p.Resolved.SecretSources, p.Dir) {
		report("warn", "%s", warning)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if _, err := exec.LookPath("git"); err == nil {
			ignored := exec.Command("git", "check-ignore", "-q", ".egzo/probe")
			ignored.Dir = dir
			if ignored.Run() != nil {
				report("warn", ".egzo/ is not git-ignored: add `.egzo/` to .gitignore, it holds clones and state that must not be committed")
			}
		}
	}
}

// --- diff -----------------------------------------------------------------------------------------

func newDiffCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show what `egzo up` would change; exit 1 when something would",
		Long: "Show what `egzo up` would change, the way it decides: by comparing the definition of every resource\n" +
			"in egzo.yaml with what runs. Silent, with exit code 0, when the project is converged.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			var buffer bytes.Buffer
			if err := stack.Up(ctx, s.engine, s.Resolved, s.Dir, stack.Options{DryRun: true, Image: imageRef(), HarnessPrefix: os.Getenv(EnvHarnessPrefix)}, &buffer); err != nil {
				return err
			}
			changes := 0
			scanner := bufio.NewScanner(&buffer)
			for scanner.Scan() {
				line := scanner.Text()
				// what a dry run says about checking, not about changing
				if line == "nothing to do" || strings.HasPrefix(line, "would check") {
					continue
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
				changes++
			}
			if changes > 0 {
				return ExitError{Code: 1}
			}
			return nil
		},
	}
}

// --- ca rotate ------------------------------------------------------------------------------------

func newCACommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "ca", Short: "The project's certificate authority"}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Issue a new project CA and restart the agents so they trust it",
		Long: "The proxy holds the project's CA key and intercepts only the hosts that get a credential injected.\n" +
			"rotate makes a new CA, publishes its certificate to the agents, and restarts them, because most\n" +
			"programs read their trust store only when they start.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			proxy, err := s.service("proxy")
			if err != nil {
				return err
			}
			if proxy.State != "running" {
				return fmt.Errorf("the proxy is %s: run `egzo up` first", proxy.State)
			}
			result, err := s.engine.Exec(ctx, proxy.ID, []string{"/egzo", "proxy", "request", "POST", "/ca/rotate"}, nil)
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("rotate the CA: %s", strings.TrimSpace(string(result.Stderr)))
			}
			var rotated struct{ CA string }
			json.Unmarshal(result.Stdout, &rotated)
			if len(rotated.CA) >= 12 {
				fmt.Fprintf(cmd.OutOrStdout(), "new CA %s\n", rotated.CA[:12])
			}
			timeout := 5
			for _, r := range s.observed.Resources {
				if r.Type == "container" && r.Kind == "agent" && r.State == "running" {
					fmt.Fprintf(cmd.OutOrStdout(), "restart %s\n", r.Instance)
					if err := s.engine.API.ContainerRestart(ctx, r.ID, container.StopOptions{Timeout: &timeout}); err != nil {
						return fmt.Errorf("restart %s: %w", r.Instance, err)
					}
				}
			}
			return nil
		},
	})
	return cmd
}

// --- proxy rules and the audit filter ---------------------------------------------------------------

func newProxyRulesCommand(opts *options) *cobra.Command {
	var only string
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Show what each agent may reach, and which credentials are injected where",
		Long: "Show the egress policy as egzo.yaml defines it, per agent: the hosts it may reach and the services\n" +
			"that get a credential injected. Secrets are shown by name, never by value.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(opts)
			if err != nil {
				return err
			}
			// The rules belong to templates; an instance has those of its template.
			templates := map[string]string{} // the name shown -> the template it is read from
			for _, name := range agentNames(p.Resolved.Agents) {
				templates[name] = name
			}
			if only != "" {
				if _, ok := p.Resolved.Agents[only]; ok {
					templates = map[string]string{only: only}
				} else if template, ok := instanceTemplate(cmd.Context(), p.Resolved.Name, only); ok && p.Resolved.Agents[template].Harness != "" {
					templates = map[string]string{only: template}
				} else {
					return fmt.Errorf("no template or instance %q (templates: %s)", only, strings.Join(agentNames(p.Resolved.Agents), ", "))
				}
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "AGENT\tPROFILE\tHOST\tACCESS")
			for _, name := range sortedNames(templates) {
				profileName := p.Resolved.Agents[templates[name]].Egress
				profile := p.Resolved.Egress[profileName]
				for _, host := range profile.Allow {
					fmt.Fprintf(table, "%s\t%s\t%s\tallowed\n", name, profileName, host)
				}
				services := make([]string, 0, len(profile.Services))
				for service := range profile.Services {
					services = append(services, service)
				}
				sort.Strings(services)
				for _, service := range services {
					definition := profile.Services[service]
					access := "allowed"
					if definition.Inject != nil {
						access = fmt.Sprintf("credential injected as %s (service %s, secret %s)", definition.Inject.Header, service, definition.Secret)
					}
					for _, host := range definition.Hosts {
						fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", name, profileName, host, access)
					}
				}
			}
			return table.Flush()
		},
	}
	cmd.Flags().StringVar(&only, "agent", "", "only this template, or the template of this instance")
	return cmd
}

// instanceTemplate finds the template an instance was spawned from, when the engine can say.
func instanceTemplate(ctx context.Context, project, name string) (string, bool) {
	c, err := engine.Connect(ctx)
	if err != nil {
		return "", false
	}
	defer c.Close()
	observed, err := stack.Observe(ctx, c, project)
	if err != nil {
		return "", false
	}
	instance, ok := observed.Instance(name)
	return instance.Template, ok
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// agentLines passes on the audit lines of one agent.
type agentLines struct {
	out    io.Writer
	agent  string
	buffer []byte
}

func (w *agentLines) Write(p []byte) (int, error) {
	w.buffer = append(w.buffer, p...)
	for {
		end := bytes.IndexByte(w.buffer, '\n')
		if end < 0 {
			break
		}
		line := w.buffer[:end]
		var event struct{ Agent string }
		if json.Unmarshal(line, &event) == nil && event.Agent == w.agent {
			w.out.Write(append(append([]byte{}, line...), '\n'))
		}
		w.buffer = w.buffer[end+1:]
	}
	return len(p), nil
}

// checkInternalNetworks makes sure the engine can make an internal network, the one thing the isolation
// of every agent stands on: no route out except through the proxy.
func checkInternalNetworks(ctx context.Context, c *engine.Client, report func(level, format string, a ...any)) {
	name := fmt.Sprintf("egzo-doctor-%d", os.Getpid())
	created, err := c.API.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge", Internal: true})
	if err != nil {
		report("fail", "network: the engine cannot create an internal network, which agents' isolation needs: %s", firstLine(err.Error()))
		return
	}
	defer c.API.NetworkRemove(context.WithoutCancel(ctx), created.ID)
	inspected, err := c.API.NetworkInspect(ctx, created.ID, network.InspectOptions{})
	if err != nil || !inspected.Internal {
		report("fail", "network: the engine made a network that is not internal: agents would have a route out")
		return
	}
	report("ok", "network: internal networks work (no route out except the proxy)")
}
