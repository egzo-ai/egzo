// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"net/url"
	"strings"
)

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
	return PathProblem(prefix)
}

// PathProblem says why a request path, as it goes on the wire (escaped), may mean another path to the server than it
// says, or returns "". An API call never needs any of it, so the proxy refuses such a path under a service's paths rather
// than decide whether it lies under one:
//
//   - a character a URL path does not carry (RFC 3986), a backslash among them, which some servers take for a '/';
//   - a percent-encoded character that needs no encoding (%2E is a '.'), a '%' (encoded twice), a backslash, a control
//     character or a byte outside ASCII (some servers normalise a fullwidth '．' to '.');
//   - a segment of dots, decoded, also before a ';' parameter (which some servers strip) or with trailing spaces.
//
// %2F and %20 stay: APIs use them in names (GitLab's group%2Fproject, a file name with a space).
func PathProblem(escaped string) string {
	for i := 0; i < len(escaped); i++ {
		char := escaped[i]
		if char != '%' {
			if !pathChar(char) {
				return "has a character a URL path does not carry"
			}
			continue
		}
		if i+2 >= len(escaped) || !isHex(escaped[i+1]) || !isHex(escaped[i+2]) {
			return "has a '%' that is not an escape"
		}
		decoded := unhex(escaped[i+1])<<4 | unhex(escaped[i+2])
		i += 2
		switch {
		case unreserved(decoded):
			return "percent-encodes a character that needs no encoding"
		case decoded == '%':
			return "is percent-encoded twice"
		case decoded == '\\':
			return "has an encoded backslash"
		case decoded < 0x20 || decoded == 0x7f:
			return "has an encoded control character"
		case decoded >= 0x80:
			return "has an encoded character outside ASCII"
		}
	}
	path, err := url.PathUnescape(escaped)
	if err != nil {
		return "is not a valid escaped path"
	}
	for _, segment := range strings.Split(path, "/") {
		segment, _, _ = strings.Cut(segment, ";")
		if strings.Contains(segment, ".") && strings.Trim(segment, ". ") == "" {
			return "has a '.' or '..' segment"
		}
	}
	return ""
}

// pathChar is a character RFC 3986 lets a path carry as it is: unreserved, a sub-delimiter, ':', '@' or '/'.
func pathChar(char byte) bool {
	return unreserved(char) || strings.IndexByte("!$&'()*+,;=:@/", char) >= 0
}

func unreserved(char byte) bool {
	return 'a' <= char && char <= 'z' || 'A' <= char && char <= 'Z' || '0' <= char && char <= '9' || strings.IndexByte("-._~", char) >= 0
}

func isHex(char byte) bool {
	return '0' <= char && char <= '9' || 'a' <= char && char <= 'f' || 'A' <= char && char <= 'F'
}

func unhex(char byte) byte {
	switch {
	case char <= '9':
		return char - '0'
	case char <= 'F':
		return char - 'A' + 10
	}
	return char - 'a' + 10
}

// PathUnder reports whether path lies under prefix, by whole segments: "/api" covers "/api" and "/api/x", not
// "/apix"; "/api/" covers "/api/x".
func PathUnder(prefix, path string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return len(path) == len(prefix) || strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/'
}
