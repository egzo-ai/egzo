#!/bin/sh
# govulncheck, failing on every vulnerability the code calls except the accepted ones below.
# govulncheck itself cannot ignore an advisory, and its -format json mode always exits 0.
set -eu

# Accepted advisories, each explained in known-issues/moby-daemon-advisories.md.
ACCEPTED='["GO-2026-4887","GO-2026-4883"]'

out=$(govulncheck -format json ./...)
called=$(printf '%s\n' "$out" | jq -s -r --argjson accepted "$ACCEPTED" '
  [ .[] | select(.finding) | .finding
    | select(.trace[0].function)   # a finding with a function frame is code that calls the vulnerable symbol
    | .osv ] | unique - $accepted | .[]')

if [ -n "$called" ]; then
	echo "govulncheck: vulnerabilities reachable from egzo, not in the accepted list:" >&2
	echo "$called" | sed 's|^|  https://pkg.go.dev/vuln/|' >&2
	exit 1
fi
echo "govulncheck: no reachable vulnerabilities besides the accepted ones"
