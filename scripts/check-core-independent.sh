#!/bin/sh
# Core never depends on the hub: the hub imports core, never the reverse (roadmap/hub.md, "Code layout and rules").
# Go's `internal` rule already stops it for anything under hub/internal; this also fails on a future move.
set -eu
cd "$(dirname "$0")/.."
found=$(go list -deps ./... | grep -E '/hub(/|$)' || true)
if [ -n "$found" ]; then
	echo "core packages depend on the hub:" >&2
	echo "$found" | sed 's|^|  |' >&2
	exit 1
fi
echo "core does not depend on the hub"
