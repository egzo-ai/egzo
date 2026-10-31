#!/bin/sh
# The hub holds no way into a machine: it speaks to projects over the link, and does not link the Docker client or core's engine code
# (roadmap/hub-link.md, "The connection"). Its tests may: they run core's real link.
set -eu
cd "$(dirname "$0")/../hub"
found=$(go list -deps ./cmd/egzo-hub | grep -E '^github.com/docker/docker|^github.com/compose-spec|/internal/(engine|stack|link)$' || true)
if [ -n "$found" ]; then
	echo "the hub links what it must not:" >&2
	echo "$found" | sed 's|^|  |' >&2
	exit 1
fi
echo "the hub links no engine code"
