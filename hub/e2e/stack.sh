#!/bin/sh
# The whole stack for real: the hub image from compose.yaml with Dex added as its single sign-on (stack.compose.yaml), a project made by
# the egzo CLI that links itself to it from the local Docker engine, and a browser in Playwright's image that signs in as admin, approves
# the project, links Dex and signs in with it. The hub mounts nothing of the host. Run it with `make hub-stack`.
set -eu
cd "$(dirname "$0")/../.."
ROOT=$(pwd)
BIN=$ROOT/bin/egzo
WORK=$(mktemp -d)
NAME=hubstack$$
export COMPOSE_PROJECT_NAME=egzo-hub-stack-$$
export EGZO_OIDC_CLIENT_SECRET=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
export EGZO_ADMIN_PASSWORD=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
export EGZO_HUB_VERSION=dev
# the address a link reaches the hub at, from a container of the engine: not localhost, which is the link itself. People use localhost.
GATEWAY=$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}')
export EGZO_LINK_URL=http://$GATEWAY:18080
export EGZO_PUBLIC_URL=http://localhost:18080
COMPOSE="docker compose -f hub/compose.yaml -f hub/e2e/stack.compose.yaml"

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

$COMPOSE up -d --wait --wait-timeout 90 >/dev/null

# `--wait` only waits for a container to run, and Dex has no health check: wait until it and the hub answer what the login needs
wait_for() {
	for _ in $(seq 1 60); do
		curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
		sleep 1
	done
	echo "hub-stack: $1 did not answer in 60s" >&2
	exit 1
}
wait_for http://localhost:15556/dex/.well-known/openid-configuration
wait_for http://localhost:18080/healthz

# the hub mounts nothing of the host
if $COMPOSE config --format json | grep -q 'docker.sock'; then
	echo "hub-stack: the compose file mounts the engine's socket" >&2
	exit 1
fi

up() {
	(cd "$WORK/$NAME" && EGZO_STACK_SECRET_KEY=hub-stack-secret-key-0123456789abcdef EGZO_IMAGE=$SIDECAR "$BIN" project up "$@")
}

# the project names the hub, and its first `up` says where to approve it
mkdir "$WORK/$NAME"
printf 'hub: %s\nsecret_key: v/EGZO_STACK_SECRET_KEY\nvaults:\n  v: { backend: env }\nagents:\n  coder:\n    harness: custom\n    image: %s\n    env:\n      FAKE_TUI: cat\n' "$EGZO_LINK_URL" "$SESSION" > "$WORK/$NAME/egzo.yaml"
OUTPUT=$(up 2>&1) || { echo "$OUTPUT" >&2; exit 1; }
CODE=$(echo "$OUTPUT" | sed -n 's|.*link: pending: register this project at .*/link/\([A-Z0-9-]*\).*|\1|p')
if [ -z "$CODE" ]; then
	echo "hub-stack: the first up gave no address to register at:" >&2
	echo "$OUTPUT" >&2
	exit 1
fi

docker build -q -t egzo-hub-e2e hub/e2e >/dev/null
browser() {
	docker run --rm --network host --ipc=host --add-host dex:127.0.0.1 -v "$ROOT/hub:/hub" \
		-e HUB_STACK_URL=http://localhost:18080 -e HUB_STACK_PROJECT="$NAME" -e HUB_STACK_CODE="$CODE" -e HUB_STACK_ADMIN_PASSWORD="$EGZO_ADMIN_PASSWORD" \
		egzo-hub-e2e pytest -q -p no:cacheprovider test_stack.py -k "$1"
}

in_ps() {
	(cd "$WORK/$NAME" && EGZO_IMAGE=$SIDECAR "$BIN" ps | grep -q "$1")
}

# approved in the browser, the project is on the hub and `egzo ps` says so
browser test_the_admin
in_ps "linked to $EGZO_LINK_URL"

# the machine goes away: the hub shows the project offline, and `up` brings it back with the same key and owner
docker stop "$NAME-link-1" >/dev/null
browser test_a_project_whose_link_stopped
docker start "$NAME-link-1" >/dev/null
for _ in $(seq 1 30); do
	(cd "$WORK/$NAME" && EGZO_IMAGE=$SIDECAR "$BIN" ps 2>/dev/null | grep -q 'locked') && break
	sleep 1
done
up >/dev/null
in_ps "linked to $EGZO_LINK_URL"
browser test_a_project_that_came_back

# `down` unlinks, and the hub forgets
(cd "$WORK/$NAME" && EGZO_STACK_SECRET_KEY=hub-stack-secret-key-0123456789abcdef EGZO_IMAGE=$SIDECAR "$BIN" project down --force >/dev/null)
browser test_a_project_that_unlinked
