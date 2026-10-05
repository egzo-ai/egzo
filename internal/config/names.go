package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Names become container, network and volume names, DNS names, labels and git refs, and the control
// sidecar accepts only a plain form of them. One rule, checked when the file is read, keeps a later
// step from failing on a name or two resources from landing on the same engine name.
var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// reservedAgents are names egzo gives to its own containers, networks, DNS aliases or directories.
var reservedAgents = map[string]bool{"control": true, "proxy": true, "prep": true, "shared": true}

// reservedWorkspaces are volumes egzo creates for itself: a workspace with one of these names would hand
// an agent the control volume (which holds the key every token derives from) or the CA the agents trust.
var reservedWorkspaces = map[string]bool{"control": true, "ca": true, "ca-private": true, "egress": true}

func checkNames(file *File, p *problems) {
	agents := make([]string, 0, len(file.Agents))
	for name := range file.Agents {
		agents = append(agents, name)
	}
	sort.Strings(agents)
	for _, name := range agents {
		switch {
		case reservedAgents[name]:
			p.addf("agent %q: %q is reserved for egzo's own use", name, name)
		case !identifier.MatchString(name):
			p.addf("agent %q: a name is 1 to 63 lowercase letters, digits, '-' and '_', starting with a letter or digit", name)
		}
	}
	workspaces := make([]string, 0, len(file.Workspaces))
	for name := range file.Workspaces {
		workspaces = append(workspaces, name)
	}
	sort.Strings(workspaces)
	for _, name := range workspaces {
		switch {
		case reservedWorkspaces[name]:
			p.addf("workspace %q: %q is reserved for egzo's own use", name, name)
		case !identifier.MatchString(name):
			p.addf("workspace %q: a name is 1 to 63 lowercase letters, digits, '-' and '_', starting with a letter or digit", name)
		case strings.HasSuffix(name, "-home") && file.Agents[strings.TrimSuffix(name, "-home")].Harness != "":
			p.addf("workspace %q: %q is reserved for egzo's own use (the home of agent %q)", name, name, strings.TrimSuffix(name, "-home"))
		}
	}
}

// checkDependencies rejects depends_on cycles, which would make `up` wait forever.
func checkDependencies(file *File, p *problems) {
	names := make([]string, 0, len(file.Agents))
	for name := range file.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	state := map[string]int{} // 1 on the path, 2 finished
	var path []string
	reported := false
	var visit func(string)
	visit = func(name string) {
		if reported || state[name] == 2 {
			return
		}
		if state[name] == 1 {
			start := 0
			for i, n := range path {
				if n == name {
					start = i
				}
			}
			p.addf("depends_on cycle: %s", strings.Join(append(append([]string{}, path[start:]...), name), " -> "))
			reported = true
			return
		}
		state[name] = 1
		path = append(path, name)
		for _, dependency := range file.Agents[name].DependsOn {
			if _, ok := file.Agents[dependency]; ok {
				visit(dependency)
			}
		}
		path = path[:len(path)-1]
		state[name] = 2
	}
	for _, name := range names {
		visit(name)
	}
}

// validBranch follows git's own rules for a ref name (git check-ref-format) and refuses a leading '-',
// which git would read as an option.
func validBranch(branch string) bool {
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// checkGit validates a git source: https only, no credentials in the URL, a real branch name.
func checkGit(name string, git *Git, p *problems) {
	if !strings.HasPrefix(strings.ToLower(git.URL), "https://") {
		p.addf("workspace %q: git url %q must be an https URL; ssh URLs and local paths are not supported", name, redact(git.URL))
	} else if parsed, err := url.Parse(git.URL); err != nil || parsed.Host == "" {
		p.addf("workspace %q: git url is not a valid URL", name)
	} else if parsed.User != nil {
		p.addf("workspace %q: the git url must not contain credentials: bind a token to the github service of an egress profile instead", name)
	}
	if git.Branch != "" && !validBranch(git.Branch) {
		p.addf("workspace %q: %q is not a valid git branch name", name, git.Branch)
	}
}

// redact hides userinfo so an error message never repeats a credential.
func redact(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.User != nil {
		parsed.User = nil
		return parsed.String()
	}
	return raw
}

// reservedEnv are the variables egzo sets to wire an agent to its sidecars: replacing one would
// silently cut the agent off from its proxy or trust store, or point it at something else.
var reservedEnv = map[string]bool{
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true, "REQUESTS_CA_BUNDLE": true,
	"CURL_CA_BUNDLE": true, "GIT_SSL_CAINFO": true, "NODE_EXTRA_CA_CERTS": true,
}

func reservedEnvName(key string) bool {
	return strings.HasPrefix(key, "EGZO_") || reservedEnv[strings.ToUpper(key)]
}

// normalizeHost lowercases a host pattern and drops a trailing dot, the way the proxy compares.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// engineSockets are where container engines listen; a directory above one hands the agent the engine.
var engineSockets = []string{"/var/run/docker.sock", "/run/docker.sock", "/run/podman/podman.sock", "/var/run/podman/podman.sock"}

// socketWarning says why mounting dir is dangerous, or returns "" when it is not.
func socketWarning(dir string) string {
	dir = filepath.Clean(dir)
	for _, socket := range engineSockets {
		if socket == dir || strings.HasPrefix(socket, strings.TrimSuffix(dir, "/")+"/") {
			return fmt.Sprintf("%s holds the container engine's socket (%s): an agent that reaches it controls the host", dir, filepath.Base(socket))
		}
	}
	return ""
}
