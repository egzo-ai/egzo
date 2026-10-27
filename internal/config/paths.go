// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import "strings"

// PathPrefixProblem says why prefix cannot limit a service to the paths of its API, or returns "". A prefix is a
// plain absolute path, so that what it covers is what the upstream server will see: no dot segments, no
// percent-encoding, no query or fragment.
func PathPrefixProblem(prefix string) string {
	switch {
	case !strings.HasPrefix(prefix, "/"):
		return "must start with '/'"
	case strings.ContainsAny(prefix, "?#%\\"):
		return "must be a plain path, without '?', '#', '%' or '\\'"
	}
	for _, char := range prefix {
		if char <= 0x20 || char == 0x7f {
			return "cannot contain a space or a control character"
		}
	}
	if AmbiguousPath(prefix) {
		return "cannot have a '.' or '..' segment"
	}
	return ""
}

// AmbiguousPath reports whether a request path (decoded) may mean another path to the server than it says: a '.'
// or '..' segment (also before a ';' parameter, which some servers strip) or a backslash, which some servers take
// for a '/'. The proxy refuses such a path rather than decide whether it lies under a prefix.
func AmbiguousPath(path string) bool {
	if strings.Contains(path, "\\") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		segment, _, _ = strings.Cut(segment, ";")
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

// PathUnder reports whether path lies under prefix, by whole segments: "/api" covers "/api" and "/api/x", not
// "/apix"; "/api/" covers "/api/x".
func PathUnder(prefix, path string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return len(path) == len(prefix) || strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/'
}
