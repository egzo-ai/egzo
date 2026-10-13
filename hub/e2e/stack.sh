#!/bin/sh
# The whole stack for real: the hub image and Dex from compose.yaml, the local Docker engine, a project made by the egzo CLI, and a
# browser in Playwright's image logging in through Dex. Run it with `make hub-stack`.
set -eu
cd "$(dirname "$0")/../.."
ROOT=$(pwd)
BIN=$ROOT/bin/egzo
WORK=$(mktemp -d)
NAME=hubstack$$
export COMPOSE_PROJECT_NAME=egzo-hub-stack-$$
export EGZO_SECRET_KEY=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
export EGZO_OIDC_CLIENT_SECRET=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
export EGZO_HUB_VERSION=dev
COMPOSE="docker compose -f hub/compose.yaml"

cleanup() {
	status=$?
	[ $status -ne 0 ] && $COMPOSE logs --tail 50 2>&1 | sed 's/^/  stack: /' >&2
	$COMPOSE down -v >/dev/null 2>&1 || true
	(cd "$WORK/$NAME" 2>/dev/null && EGZO_IMAGE=$SIDECAR "$BIN" project down --force >/dev/null 2>&1) || true
	rm -rf "$WORK"
	exit $status
}
trap cleanup EXIT

# images: the sidecar (the egzo binary), and an agent that runs the stand-in terminal program of the specs
mkdir "$WORK/sidecar" "$WORK/session"
cp "$BIN" "$WORK/sidecar/egzo"
printf 'FROM docker.io/library/alpine:3\nRUN apk add --no-cache ca-certificates git\nCOPY egzo /egzo\n' > "$WORK/sidecar/Dockerfile"
SIDECAR=egzo-hub-stack-sidecar:$(sha256sum "$BIN" | cut -c1-12)
docker build -q -t "$SIDECAR" "$WORK/sidecar" >/dev/null
cp "$BIN" "$WORK/session/egzo"
cp specs/fixtures/fake-tui.sh "$WORK/session/fake-tui"
printf 'FROM docker.io/library/alpine:3\nRUN apk add --no-cache curl git\nCOPY fake-tui /usr/local/bin/fake-tui\nCOPY egzo /usr/local/bin/egzo\nENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]\nCMD ["/usr/local/bin/fake-tui"]\n' > "$WORK/session/Dockerfile"
SESSION=egzo-hub-stack-session:$(sha256sum "$BIN" specs/fixtures/fake-tui.sh | sha256sum | cut -c1-12)
docker build -q -t "$SESSION" "$WORK/session" >/dev/null

mkdir "$WORK/$NAME"
printf 'agents:\n  coder:\n    harness: custom\n    image: %s\n    env:\n      FAKE_TUI: cat\n' "$SESSION" > "$WORK/$NAME/egzo.yaml"
(cd "$WORK/$NAME" && EGZO_IMAGE=$SIDECAR "$BIN" project up >/dev/null)

$COMPOSE up -d --wait --wait-timeout 90 >/dev/null

docker build -q -t egzo-hub-e2e hub/e2e >/dev/null
docker run --rm --network host --ipc=host --add-host dex:127.0.0.1 -v "$ROOT/hub:/hub" \
	-e HUB_STACK_URL=http://localhost:8080 -e HUB_STACK_PROJECT="$NAME" \
	egzo-hub-e2e pytest -q -p no:cacheprovider test_stack.py
