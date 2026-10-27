#!/usr/bin/env bash
# The versions pinned in ARG lines of the Dockerfiles (Dependabot cannot see them), and where their releases come from. Without
# arguments it prints which are behind; with --pr it opens, or updates, one pull request per pin on a branch bump/<pin>, from
# origin/main. .github/workflows/bump.yml runs it weekly with --pr.
#
# A release is only taken once it is COOLDOWN_DAYS old, so one that is pulled soon after it is published (a hijacked package) is
# not proposed. A version that does not look like one (digits and dots) is never written into a file.
#
# Node is pinned with the hashes of its two builds (harness/base checks the download against them): a Node bump writes all three,
# from SHASUMS256.txt once its signature checks against the Node release team's active keys (nodejs/release-keys).
set -euo pipefail

COOLDOWN_DAYS=${COOLDOWN_DAYS:-3}
cutoff=$(( $(date +%s) - COOLDOWN_DAYS * 86400 ))

# ARG | files | source | what it is (for the commit and the pull request)
PINS='
CLAUDE_CODE_VERSION|harness/claude-code/Dockerfile|npm:@anthropic-ai/claude-code|Claude Code
OPENCODE_VERSION|harness/opencode/Dockerfile|npm:@opencode/cli|OpenCode
CODEX_VERSION|harness/codex/Dockerfile|npm:@openai/codex|Codex
PLAYWRIGHT_MCP_VERSION|harness/base/Dockerfile|npm:@playwright/mcp|Playwright MCP (and its Chromium)
NODE_VERSION|harness/base/Dockerfile|node|Node
GO_VERSION|Dockerfile hub/Dockerfile|go|Go
'

# The newest stable version of an npm package published before the cutoff.
latest_npm() {
	curl -fsSL "https://registry.npmjs.org/$1" | jq -r --argjson cutoff "$cutoff" '
		.time | to_entries[]
		| select(.key | test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))
		| select(.value | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601 <= $cutoff)
		| .key' | sort -V | tail -1
}

# The newest LTS release of the Node major pinned in harness/base released before the cutoff: a new major is a decision, not a bump.
latest_node() {
	local major
	major=$(current NODE_VERSION harness/base/Dockerfile | cut -d. -f1)
	curl -fsSL https://nodejs.org/dist/index.json | jq -r --argjson cutoff "$cutoff" --arg major "v$major." '
		.[] | select(.lts != false and (.version | startswith($major)))
		| select(.date + "T00:00:00Z" | fromdateiso8601 <= $cutoff)
		| .version | ltrimstr("v")' | sort -V | tail -1
}

# The SHA-256 of Node <version>'s Linux build for <arch> (x64, arm64), from its signed checksums.
node_sha256() {
	local work
	work=$(mktemp -d)
	curl -fsSL -o "$work/SHASUMS256.txt.asc" "https://nodejs.org/dist/v$1/SHASUMS256.txt.asc"
	curl -fsSL -o "$work/keys.kbx" https://github.com/nodejs/release-keys/raw/HEAD/gpg-only-active-keys/pubring.kbx
	gpgv --keyring "$work/keys.kbx" "$work/SHASUMS256.txt.asc" 2>/dev/null || { echo "Node $1: SHASUMS256.txt is not signed by a release key" >&2; rm -rf "$work"; return 1; }
	gpg --batch --quiet --no-default-keyring --keyring "$work/keys.kbx" --decrypt "$work/SHASUMS256.txt.asc" 2>/dev/null \
		| awk -v f="node-v$1-linux-$2.tar.xz" '$2 == f && $1 ~ /^[0-9a-f]{64}$/ {print $1}'
	rm -rf "$work"
}

# The newest stable Go that has a golang:<version>-alpine image (what the Dockerfiles build with). Go releases come from the Go
# team's own infrastructure and the image tags are rebuilt often, so there is no cooldown here.
latest_go() {
	for version in $(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[].version | ltrimstr("go")' | sort -rV); do
		if curl -fsS -o /dev/null "https://hub.docker.com/v2/repositories/library/golang/tags/$version-alpine"; then
			echo "$version"
			return
		fi
	done
}

latest() {
	case "$1" in
	npm:*) latest_npm "${1#npm:}" ;;
	node) latest_node ;;
	go) latest_go ;;
	esac
}

current() { sed -n "s/^ARG $1=//p" "$2" | head -1; }

pr=false
[ "${1:-}" = --pr ] && pr=true
if $pr; then
	git fetch -q origin main '+refs/heads/bump/*:refs/remotes/origin/bump/*'
fi

status=0
while IFS='|' read -r arg files source label; do
	[ -n "$arg" ] || continue
	read -r -a paths <<<"$files"
	have=$(current "$arg" "${paths[0]}")
	want=$(latest "$source" || true)
	if ! [[ "$want" =~ ^[0-9]+(\.[0-9]+)+$ ]]; then
		echo "$arg: no usable version from $source (got '$want')" >&2
		status=1
		continue
	fi
	if [ "$have" = "$want" ] || [ "$(printf '%s\n%s\n' "$have" "$want" | sort -V | tail -1)" = "$have" ]; then
		echo "$arg $have: current"
		continue
	fi
	echo "$arg $have -> $want"
	$pr || continue

	branch=bump/$(echo "$arg" | tr 'A-Z_' 'a-z-')
	if git rev-parse -q --verify "origin/$branch" >/dev/null &&
		[ "$(git show "origin/$branch:${paths[0]}" | sed -n "s/^ARG $arg=//p" | head -1)" = "$want" ]; then
		echo "  $branch already proposes $want"
		continue
	fi
	edits=("s/^ARG $arg=.*/ARG $arg=$want/")
	if [ "$source" = node ]; then
		amd64=$(node_sha256 "$want" x64) && arm64=$(node_sha256 "$want" arm64) && [ -n "$amd64" ] && [ -n "$arm64" ] || {
			echo "  Node $want: no verified hashes, not proposed" >&2
			status=1
			continue
		}
		edits+=("s/^ARG NODE_SHA256_AMD64=.*/ARG NODE_SHA256_AMD64=$amd64/" "s/^ARG NODE_SHA256_ARM64=.*/ARG NODE_SHA256_ARM64=$arm64/")
	fi
	git switch -q -C "$branch" origin/main
	for edit in "${edits[@]}"; do sed -i "$edit" "${paths[@]}"; done
	title="$label $want"
	git commit -q -m "$title" -m "Bumped by scripts/bump-pins.sh: $arg from $have to $want." -- "${paths[@]}"
	git push -q -f origin "$branch"
	body="\`$arg\` from $have to $want (\`${files}\`), at least $COOLDOWN_DAYS days after its release.

Opened by \`.github/workflows/bump.yml\`. The specs build the real images with it; merge when they pass, and it ships with the next release."
	if [ "$(gh pr view "$branch" --json state -q .state 2>/dev/null || true)" = OPEN ]; then
		gh pr edit "$branch" --title "$title" --body "$body" >/dev/null
		echo "  updated the pull request of $branch"
	else
		gh pr create --base main --head "$branch" --title "$title" --body "$body"
	fi
done <<<"$PINS"
$pr && git switch -q --detach origin/main
exit $status
