// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"slices"
	"sort"
	"strings"
)

// ResolvedProfile is an egress profile after extend and built-in services are applied.
type ResolvedProfile struct {
	Allow    []string                    `yaml:"allow"`
	Services map[string]*ResolvedService `yaml:"services"`
}

type ResolvedService struct {
	Hosts  []string `yaml:"hosts"`
	Inject *Inject  `yaml:"inject,omitempty"`
	Secret string   `yaml:"secret,omitempty"`
	// Placeholder is the variable the agents of the profile get, holding a stand-in the proxy swaps for the secret.
	Placeholder string `yaml:"placeholder,omitempty"`
	Inspect     bool   `yaml:"inspect,omitempty"`
}

const defaultProfile = "default"

type egressResolver struct {
	file     *File
	problems *problems
	done     map[string]*ResolvedProfile
	visiting map[string]bool
}

// resolveEgress resolves every profile, plus an empty default when none is defined: an
// undefined default denies everything.
func resolveEgress(file *File, p *problems) map[string]*ResolvedProfile {
	r := &egressResolver{
		file:     file,
		problems: p,
		done:     map[string]*ResolvedProfile{},
		visiting: map[string]bool{},
	}
	names := []string{defaultProfile}
	for name := range file.Egress {
		if name != defaultProfile {
			names = append(names, name)
		}
	}
	sort.Strings(names[1:])
	for _, name := range names {
		r.resolve(name, nil)
	}
	return r.done
}

func (r *egressResolver) resolve(name string, chain []string) *ResolvedProfile {
	if profile, ok := r.done[name]; ok {
		return profile
	}
	if r.visiting[name] {
		cycle := append(slices.Clone(chain[slices.Index(chain, name):]), name)
		r.problems.addf("egress profile extend cycle: %s", strings.Join(cycle, " -> "))
		return nil
	}
	definition, defined := r.file.Egress[name]
	if !defined && name != defaultProfile {
		return nil
	}

	r.visiting[name] = true
	defer delete(r.visiting, name)

	current := &ResolvedProfile{Services: map[string]*ResolvedService{}}
	if definition.Extend != "" {
		_, parentDefined := r.file.Egress[definition.Extend]
		if !parentDefined && definition.Extend != defaultProfile {
			r.problems.addf("egress profile %q extends unknown profile %q", name, definition.Extend)
		} else if parent := r.resolve(definition.Extend, append(chain, name)); parent != nil {
			current.Allow = slices.Clone(parent.Allow)
			for service, resolved := range parent.Services {
				copied := *resolved
				copied.Hosts = slices.Clone(resolved.Hosts)
				current.Services[service] = &copied
			}
		}
	}

	for _, entry := range definition.Allow {
		entry = normalizeHost(entry)
		if !validAllow(entry) {
			r.problems.addf("egress profile %q: invalid allow entry %q (use a host, *.domain or *)", name, entry)
			continue
		}
		if !slices.Contains(current.Allow, entry) {
			current.Allow = append(current.Allow, entry)
		}
	}

	serviceNames := make([]string, 0, len(definition.Services))
	for service := range definition.Services {
		serviceNames = append(serviceNames, service)
	}
	sort.Strings(serviceNames)
	for _, service := range serviceNames {
		r.applyService(name, current, service, definition.Services[service])
	}

	r.checkProfile(name, current)
	r.done[name] = current
	return current
}

func (r *egressResolver) applyService(profile string, current *ResolvedProfile, name string, entry ServiceEntry) {
	if entry.Def == nil {
		existing := current.Services[name]
		if existing == nil {
			builtin, ok := builtinService(name)
			if !ok {
				known := append(builtinNames(), serviceKeys(current)...)
				if close := suggest(name, known); close != "" {
					r.problems.addf("egress profile %q: unknown service %q (did you mean %q?)", profile, name, close)
				} else {
					r.problems.addf("egress profile %q: unknown service %q (built-in services: %s)", profile, name, strings.Join(builtinNames(), ", "))
				}
				return
			}
			current.Services[name] = builtin
			existing = builtin
		}
		if existing.Inject == nil && existing.Placeholder == "" {
			r.problems.addf("egress profile %q: service %q has neither inject nor placeholder, so a secret cannot be bound to it", profile, name)
			return
		}
		if r.checkSecret(profile, name, entry.Ref) {
			existing.Secret = entry.Ref
		}
		return
	}

	def := *entry.Def
	def.Hosts = make([]string, len(entry.Def.Hosts))
	for i, host := range entry.Def.Hosts {
		def.Hosts[i] = normalizeHost(host)
	}
	valid := true
	if len(def.Hosts) == 0 {
		r.problems.addf("egress profile %q: service %q needs hosts", profile, name)
		valid = false
	}
	for _, host := range def.Hosts {
		if !validHost(host) {
			r.problems.addf("egress profile %q: service %q has an invalid host %q", profile, name, host)
			valid = false
		}
	}
	if def.Inject != nil && def.Inject.Header == "" {
		r.problems.addf("egress profile %q: service %q: inject needs a header", profile, name)
		valid = false
	}
	switch {
	case def.Inject != nil && def.Placeholder != "":
		r.problems.addf("egress profile %q: service %q has both inject and placeholder; a service has one or the other", profile, name)
		valid = false
	case def.Placeholder != "" && def.Secret == "":
		r.problems.addf("egress profile %q: service %q has a placeholder but no secret to stand for", profile, name)
		valid = false
	case def.Inject == nil && def.Placeholder == "" && def.Secret != "":
		r.problems.addf("egress profile %q: service %q has a secret but neither inject nor placeholder, so nothing would use it", profile, name)
		valid = false
	}
	if def.Placeholder != "" {
		if reason := placeholderProblem(def.Placeholder); reason != "" {
			r.problems.addf("egress profile %q: service %q: placeholder %q %s", profile, name, def.Placeholder, reason)
			valid = false
		}
	}
	if def.Secret != "" && !r.checkSecret(profile, name, def.Secret) {
		valid = false
	}
	if !valid {
		return
	}
	resolved := &ResolvedService{Hosts: slices.Clone(def.Hosts), Secret: def.Secret, Placeholder: def.Placeholder, Inspect: def.Inspect}
	if def.Inject != nil {
		inject := *def.Inject
		resolved.Inject = &inject
	}
	current.Services[name] = resolved
}

func serviceKeys(profile *ResolvedProfile) []string {
	names := make([]string, 0, len(profile.Services))
	for name := range profile.Services {
		names = append(names, name)
	}
	return names
}

// checkSecret validates a <vault>/<secret> reference against the declared vaults. The reference is
// split at the first '/': a vault name has none, a secret name can.
func (r *egressResolver) checkSecret(profile, service, ref string) bool {
	vaultName, secret, found := strings.Cut(ref, "/")
	if !found || vaultName == "" || secret == "" {
		r.problems.addf("egress profile %q: service %q: invalid secret reference %q (want <vault>/<secret>)", profile, service, ref)
		return false
	}
	vault, ok := r.file.Vaults[vaultName]
	if !ok {
		r.problems.addf("egress profile %q: service %q: secret reference %q names unknown vault %q", profile, service, ref, vaultName)
		return false
	}
	if !slices.Contains(vault.Secrets, secret) {
		r.problems.addf("egress profile %q: service %q: secret reference %q: vault %q does not list %q", profile, service, ref, vaultName, secret)
		return false
	}
	return true
}

// placeholderProblem says why name cannot be a placeholder variable, or returns "".
func placeholderProblem(name string) string {
	switch {
	case !envName.MatchString(name):
		return "is not a valid environment variable name"
	case placeholderReserved(name):
		return "is a variable egzo sets itself"
	}
	return ""
}

// checkProfile enforces the rules that need the fully merged profile.
func (r *egressResolver) checkProfile(name string, profile *ResolvedProfile) {
	names := serviceKeys(profile)
	sort.Strings(names)
	for _, service := range names {
		resolved := profile.Services[service]
		if (resolved.Inject != nil || resolved.Placeholder != "") && resolved.Secret == "" {
			r.problems.addf("egress profile %q: service %q needs a secret; bind one as %s: <vault>/<secret>", name, service, service)
		}
	}
	holders := map[string]string{}
	for _, service := range names {
		placeholder := profile.Services[service].Placeholder
		if placeholder == "" {
			continue
		}
		if other, taken := holders[placeholder]; taken {
			r.problems.addf("egress profile %q: services %q and %q share the placeholder variable %s", name, other, service, placeholder)
			continue
		}
		holders[placeholder] = service
	}
	for i, a := range names {
		for _, b := range names[i+1:] {
			left, right := profile.Services[a], profile.Services[b]
			if sameInjection(left, right) {
				continue
			}
			for _, host := range left.Hosts {
				if slices.Contains(right.Hosts, host) {
					r.problems.addf("egress profile %q: services %q and %q both cover host %q with different injection", name, a, b, host)
				}
			}
		}
	}
}

func sameInjection(a, b *ResolvedService) bool {
	if a.Secret != b.Secret {
		return false
	}
	switch {
	case a.Inject == nil && b.Inject == nil:
		return true
	case a.Inject == nil || b.Inject == nil:
		return false
	}
	return *a.Inject == *b.Inject
}

func validHost(host string) bool {
	return host != "" && !strings.ContainsAny(host, " \t/*:") && !strings.Contains(host, "://")
}

func validAllow(entry string) bool {
	switch {
	case entry == "*":
		return true
	case strings.HasPrefix(entry, "*."):
		return validHost(strings.TrimPrefix(entry, "*."))
	}
	return validHost(entry)
}

// reaches reports whether a profile lets an agent reach host.
func (p *ResolvedProfile) reaches(host string) bool {
	for _, pattern := range p.Allow {
		if MatchHost(pattern, host) {
			return true
		}
	}
	for _, service := range p.Services {
		if slices.Contains(service.Hosts, host) {
			return true
		}
	}
	return false
}

// MatchHost reports whether an allow pattern (a host, *.domain or *) covers host.
func MatchHost(pattern, host string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*."):
		return strings.HasSuffix(host, pattern[1:])
	}
	return pattern == host
}
