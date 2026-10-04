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
	problems = append(problems, checkCredentialKinds(project, values)...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return values, nil
}

// checkCredentialKinds catches the one mix-up that is certain to fail and is easy to make: a Claude
// subscription token (`claude setup-token`, sk-ant-oat...) is not an API key. api.anthropic.com takes the
// token only as `Authorization: Bearer` and a key only as x-api-key; sent the other way it answers 401
// "API key is invalid". The message never contains the value.
func checkCredentialKinds(project *config.Resolved, values map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, agent := range project.Agents {
		for name, service := range project.Egress[agent.Egress].Services {
			if service.Inject == nil || service.Secret == "" || !contains(service.Hosts, "api.anthropic.com") || seen[name+service.Secret] {
				continue
			}
			seen[name+service.Secret] = true
			value := values[service.Secret]
			bearer := strings.EqualFold(service.Inject.Header, "Authorization")
			switch {
			case !bearer && strings.HasPrefix(value, "sk-ant-oat"):
				problems = append(problems, fmt.Sprintf("secret %s is a Claude subscription (OAuth) token, but service %q sends it as %s, "+
					"which api.anthropic.com only accepts for API keys: bind the secret to the anthropic-oauth service instead", service.Secret, name, service.Inject.Header))
			case bearer && strings.HasPrefix(value, "sk-ant-api"):
				problems = append(problems, fmt.Sprintf("secret %s is an Anthropic API key, but service %q sends it as a bearer token, "+
					"which api.anthropic.com only accepts for subscription tokens: bind the secret to the anthropic service instead", service.Secret, name))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// SecretFilePath is where a file: secret lives: ~ is the home directory and a relative path is
// relative to the project directory.
func SecretFilePath(location, dir string) string {
	path := location
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path
}

// ReadSecret reads the value a secret source points to.
func ReadSecret(source, dir string) (string, error) { return readSecret(source, dir) }

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
		path := SecretFilePath(location, dir)
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
