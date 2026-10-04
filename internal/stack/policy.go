package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/proxy"
)

// BuildPolicy turns the resolved egress profiles into the policy the proxy enforces, one entry per
// agent. It embeds secret values, so it must only travel through exec stdin, never a label,
// environment variable or file visible to `inspect`.
func BuildPolicy(project *config.Resolved, tokens, secrets map[string]string) proxy.Policy {
	policy := proxy.Policy{Agents: map[string]proxy.AgentPolicy{}}
	for _, name := range sortedKeys(project.Agents) {
		profile := project.Egress[project.Agents[name].Egress]
		agent := proxy.AgentPolicy{Token: tokens[name], Allow: append([]string{}, profile.Allow...)}
		for _, serviceName := range sortedKeys(profile.Services) {
			service := profile.Services[serviceName]
			entry := proxy.Service{Name: serviceName, Hosts: service.Hosts, Secret: secrets[service.Secret], Inspect: service.Inspect}
			if service.Inject != nil {
				entry.Header, entry.Value = service.Inject.Header, service.Inject.Value
			}
			agent.Services = append(agent.Services, entry)
		}
		policy.Agents[name] = agent
	}
	data, _ := json.Marshal(policy)
	sum := sha256.Sum256(data)
	policy.Hash = hex.EncodeToString(sum[:])
	return policy
}

// ResolveSecrets reads the value of every secret an agent's profile injects. A secret that cannot
// be read is an error: an agent must not start without the credentials its profile promises.
func ResolveSecrets(project *config.Resolved, dir string) (map[string]string, error) {
	needed := map[string]bool{}
	for _, agent := range project.Agents {
		for _, service := range project.Egress[agent.Egress].Services {
			if service.Inject != nil && service.Secret != "" {
				needed[service.Secret] = true
			}
		}
	}
	values := map[string]string{}
	var problems []string
	refs := make([]string, 0, len(needed))
	for ref := range needed {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		value, err := readSecret(project.SecretSources[ref], dir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("secret %s: %v", ref, err))
			continue
		}
		values[ref] = value
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return values, nil
}

func readSecret(source, dir string) (string, error) {
	scheme, location, _ := strings.Cut(source, ":")
	switch scheme {
	case "env":
		value, ok := os.LookupEnv(location)
		if !ok || value == "" {
			return "", fmt.Errorf("environment variable %s is not set", location)
		}
		return value, nil
	case "file":
		path := location
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("cannot read %s: %v", path, err)
		}
		value := strings.TrimRight(string(data), "\r\n")
		if value == "" {
			return "", fmt.Errorf("%s is empty", path)
		}
		return value, nil
	}
	return "", fmt.Errorf("unsupported source %q", source)
}
