#!/usr/bin/env bash
# The versions pinned in ARG lines of the Dockerfiles (Dependabot cannot see them), and where their releases come from. Without
# arguments it prints which are behind; with --pr it opens, or updates, one pull request per pin on a branch bump/<pin>, from
# origin/main. .github/workflows/bump.yml runs it weekly with --pr.
#
# A release is only taken once it is COOLDOWN_DAYS old, so one that is pulled soon after it is published (a hijacked package) is
# not proposed. A version that does not look like one (digits and dots) is never written into a file.
set -euo pipefail

COOLDOWN_DAYS=${COOLDOWN_DAYS:-3}
cutoff=$(( $(date +%s) - COOLDOWN_DAYS * 86400 ))

# ARG | files | source | what it is (for the commit and the pull request)
PINS='
CLAUDE_CODE_VERSION|harness/claude-code/Dockerfile|npm:@anthropic-ai/claude-code|Claude Code
OPENCODE_VERSION|harness/opencode/Dockerfile|npm:@opencode/cli|OpenCode
PLAYWRIGHT_MCP_VERSION|harness/base/Dockerfile|npm:@playwright/mcp|Playwright MCP (and its Chromium)
GIT_VERSION|harness/base/Dockerfile|git|git
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

# The newest git release tarball on kernel.org (where harness/base downloads it from) published before the cutoff.
latest_git() {
	curl -fsSL https://www.kernel.org/pub/software/scm/git/ \
		| sed -n 's|.*>git-\([0-9]*\.[0-9]*\.[0-9]*\)\.tar\.xz</a> *\([0-9]*-[A-Za-z]*-[0-9]* [0-9:]*\).*|\1 \2|p' \
		| while read -r version day time; do
			[ "$(date -d "${day//-/ } $time" +%s)" -le "$cutoff" ] && echo "$version"
		done | sort -V | tail -1
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
	git) latest_git ;;
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
	git switch -q -C "$branch" origin/main
	sed -i "s/^ARG $arg=.*/ARG $arg=$want/" "${paths[@]}"
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
