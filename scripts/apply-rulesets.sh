#!/bin/sh
# Apply .github/rulesets/*.json to the repository: a ruleset with the file's name is replaced, a missing one is created.
# The rules of `main` live in .github/rulesets/main.json and nowhere else; change the file, then run this with an admin's `gh`.
# Usage: scripts/apply-rulesets.sh [owner/repo]   (default: the repository of the current directory)
set -eu
repo=${1:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}
cd "$(dirname "$0")/../.github/rulesets"
for file in *.json; do
	name=$(jq -r .name "$file")
	id=$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name == \"$name\") | .id")
	if [ -n "$id" ]; then
		gh api -X PUT "repos/$repo/rulesets/$id" --input "$file" --jq '"updated \(.name) (\(.id))"'
	else
		gh api -X POST "repos/$repo/rulesets" --input "$file" --jq '"created \(.name) (\(.id))"'
	fi
done
