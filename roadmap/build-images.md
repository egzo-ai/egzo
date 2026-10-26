# Roadmap: `build:` on an agent (Compose style)

**Status.** Being built: the specs are the status. How egzo builds was decided on 2026-10-26 (see "How egzo builds").

## Why

An agent that needs a compiler, a language runtime or a team's tools needs its own image. Today the user builds it by hand
`FROM` a stock harness image, pushes or tags it, and names it in `image:` ([docs/images.md](../docs/images.md)). The two steps
that can drift are the ones egzo could do: the build, and keeping the in-container `egzo` binary at the CLI's version.

## What it looks like

```yaml
agents:
  coder:
    harness: claude-code
    build:
      context: ./agent            # relative to egzo.yaml; default "."
      dockerfile: Dockerfile      # relative to the context; default "Dockerfile"
      args: { PYTHON_VERSION: "3.13" }
      target: dev                 # optional
    workspaces: [repo]
```

`build:` and `image:` exclude each other on one agent. The keys are Compose's, so nobody learns a second dialect.

## How it works

- **`egzo project up` builds, `egzo agent spawn` finds.** `up` sends the build context to the engine (Docker and Podman both have the
  build endpoint) and tags the result by content: `egzo-build/<project>-<agent>:<hash>`, the hash covering the context (after
  `.dockerignore`), the Dockerfile, the args, the target and the harness image. The tag goes into the published template, as
  `image:` does, so a changed file makes running instances stale (`up`, `agent rm`, spawn again) and an unchanged one is not
  rebuilt: the engine's layer cache and an existing tag are checked first.
- **The hub only spawns.** It spawns from published templates, which exist only after `up` on that engine, so the image is already
  there. Neither the Dockerfile nor the context ever leaves the machine that ran `up`.
- **The harness image is a build argument.** egzo passes the stock image of the agent's harness, at the CLI's version, as
  `EGZO_HARNESS_IMAGE` (the name is open), so a Dockerfile can start from it and the in-container binary always matches the CLI:
  ```dockerfile
  ARG EGZO_HARNESS_IMAGE
  FROM ${EGZO_HARNESS_IMAGE}
  USER root
  RUN apt-get update && apt-get install -y --no-install-recommends python3 build-essential
  ```
- **A missing image is explained.** The tag can disappear after `up` (`image prune`, a manual `rmi`). A spawn that cannot find a
  built image says "the image `up` built for agent X is gone: run `egzo project up` again" instead of the registry pull error
  that a hash tag would otherwise produce.

## How egzo builds

egzo sends the context as a tar to the engine's build endpoint (`POST /build`), the one call that both engines answer: Docker
runs it on BuildKit, built into the engine (`version=2`; never the legacy builder), Podman on buildah. No `docker` or `podman`
binary and no buildx plugin is needed: egzo keeps needing an engine socket and nothing else ([docs/install.md](../docs/install.md)).

- **The context.** The context directory less what `.dockerignore` excludes (the Dockerfile and `.dockerignore` are always sent),
  owned by uid and gid 0 in the tar, as the docker CLI does. The same bytes are what the content hash covers.
- **Base images, on Docker.** BuildKit reached through `/build` has no session, and the session is what lets it fetch from a
  registry: a `FROM` of an image that is not on the engine fails with `failed to resolve source metadata for <image>: no active
  sessions`. egzo pulls the harness image (`EGZO_HARNESS_IMAGE`) before every build, with the credentials it already uses for
  pulls. Any other image a build reports missing in that error is pulled the same way and the build is tried again, once per
  image. The error text is not an API: a spec pins it, so a Docker release that changes it turns red instead of breaking builds
  quietly. Podman fetches its base images itself.
- **`# syntax=` is not supported on Docker** (the parser image is fetched the same way and fails the same way). The built-in
  parser already handles heredocs and `RUN --mount`. egzo says so when the error names a `docker/dockerfile` image.
- **Output.** Podman streams plain lines. Docker's BuildKit streams its progress as `moby.buildkit.trace` messages, a base64
  protobuf (`StatusResponse`); egzo decodes the step names (`Vertex.name`, field 3) and the output of each step (`VertexLog.msg`,
  field 4) with `protowire`, no BuildKit import, and prints them during `up`. Without that, a failed `RUN` would only say its exit
  code. The final error of either engine is a plain `error` message.

### What was tested (2026-10-26, Docker 29.1.3 with the containerd image store, rootless Podman 5.7.0)

The same Dockerfile (`RUN --mount=type=cache`, a heredoc in `RUN`, `COPY`), sent by hand to each engine's socket:

| Request | Result |
|---|---|
| Docker `/build?version=2`, tar body, no session | built and tagged; the image runs; cache mount and heredoc work |
| Docker `/build?version=1` (legacy builder) | `the --mount option requires BuildKit` |
| Docker, `FROM` an image not on the engine (`alpine:3.19`, a ghcr image); also with `pull=1` | `no active sessions`; the same build passes once the image is pulled |
| Docker, `# syntax=docker/dockerfile:1` | `no active sessions` for `docker.io/docker/dockerfile:1` |
| Docker, a failing `RUN` | plain `error`: `process "…" did not complete successfully: exit code: 3`; its output (`boom`) only in the trace |
| Podman compat `/build` (`version` ignored) | built, base image pulled by Podman, cache mount and heredoc work, plain output |
| `docker build .` (CLI 29.1.3, no buildx plugin, Ubuntu's `docker.io` package) | legacy builder: `the --mount option requires BuildKit` |
| `DOCKER_BUILDKIT=1 docker build .` (same) | refused: `BuildKit is enabled but the buildx component is missing or broken` |

### How Docker Compose builds (v2.40.3, from its source)

`pkg/compose/build*.go`, in order of preference:

1. **Bake, the default:** it runs `docker buildx bake --file - --progress rawjson --metadata-file …` as a subprocess, with a bake
   file it generates on stdin; it needs the buildx plugin (0.17 or later). A comment says the long-term plan is to drop buildx as
   a Go dependency.
2. **The buildx library** (buildx missing, or `COMPOSE_BAKE=false`, deprecated): buildx's `docker` driver opens BuildKit's control
   API through the engine (`DialHijack` to `/grpc`) and a session (`/session`) that gives BuildKit registry credentials, the
   local context by file sync, secrets and ssh. Podman has no `/grpc`.
3. **Classic** (BuildKit turned off): `ImageBuild` with a tar from `moby/go-archive` and `.dockerignore` read with docker/cli's
   helpers, on the legacy builder.

egzo's call sits between 2 and 3: the endpoint of 3 with BuildKit, and the base images that 2 fetches through its session
pulled by egzo beforehand.

### Rejected

- **Running `docker build` or `podman build`** (Compose's default, through bake). The least code, but egzo would need the
  right CLI pointed at the right engine (`DOCKER_HOST`, contexts, TLS; a `docker` CLI against Podman uses the legacy builder),
  and the result would depend on what is installed: on this machine `docker build` cannot build the Dockerfile the API built.
  The exit code would also be all egzo gets, so it could not say "run `up` again".
- **Embedding buildah** (v1.46.0): needs cgo (`go.podman.io/storage`'s `pkg/unshare` and `pkg/idtools`; egzo builds with
  `CGO_ENABLED=0`), 130 direct dependencies with BuildKit among them, and builds into the local `containers-storage`: Docker would
  not see the image without loading gigabytes into it on every build, and a remote engine never would. Running BuildKit's daemon
  inside egzo has the same problems, and needs runc and a snapshotter.
- **The legacy builder** (`version=1`): no `RUN --mount`, no heredocs, deprecated by Docker.

### Later: a BuildKit session on Docker

Compose's path 2 without buildx: BuildKit's own client (`moby/buildkit/client` and `session`, pure Go) over `/grpc` and
`/session`, with a credentials provider reading `~/.docker/config.json`. It removes the pre-pull and its retry on an error text,
makes `# syntax` work, gives typed progress and sends only changed files of the context. The cost: BuildKit and gRPC as
dependencies, and a second code path, since Podman still needs `/build`. Worth it if the retry proves fragile or Dockerfiles
with images other than the harness image become common.

## What the user is responsible for

egzo builds the Dockerfile it is given and adds nothing to it. An image `FROM` a stock harness image keeps the harness
integration, the `egzo` binary, the browser and the rest of what the stock image carries ([docs/images.md](../docs/images.md)). An
image built from anything else carries only what its Dockerfile puts there: the `egzo` binary and the entrypoint if the agent is
to report to the control, and any tool the agent needs, a browser included. The browser is registered only when
`playwright-mcp` is on the `PATH`, so an image without it has no browser rather than a broken one.

The contract such an image has to meet, for `build:` and for `image:` with the `custom` harness alike (and the reason for a
possible `egzo system check --image X`):

- the `egzo` binary at `/usr/local/bin/egzo` (`COPY --from=ghcr.io/egzo-ai/egzo:<version> /egzo /usr/local/bin/egzo`);
- `ENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]`, which starts the session holder and reports status to the control
  (`egzo state <state> --reason …`, or `POST /v1/state`; messages and tools over `http://control:7777/mcp` with `EGZO_CONTROL_URL`,
  `EGZO_AGENT` and `EGZO_TOKEN`);
- `HOME=/home/agent` and a `/workspace` writable by any uid (egzo runs the agent as the invoking user, whatever `USER` says);
- `git`, `ca-certificates` and `procps`, and the CA bundle path the integrations read.

## Decisions to make

- **Build network.** A build runs in the engine, outside the egress proxy: its `RUN` steps reach anything the engine can. That
  matches hand-built images today ("installing at build time needs no egress rule", [docs/images.md](../docs/images.md)), but
  `build:` makes it part of `up`, so [docs/security.md](../docs/security.md) says it in one line: the Dockerfile is trusted.
- **Build output.** Podman's build output is not always valid UTF-8 (the specs already met it); read it as bytes. Docker's
  is decoded from BuildKit's trace (see "How egzo builds").
- **Cache and disk.** Old hash tags pile up. `project down` could remove the tags of its project; `egzo system prune` is the
  alternative. Keep the last tag of each agent and remove the rest at `up`.
- **Secrets at build time.** None: egzo passes no credentials to a build. BuildKit secrets are the user's tool.
- **Rejected:** `packages:` and `setup:` keys (egzo would become a package manager); building through the proxy; pushing the
  result to a registry (use `docker build` and `image:` for that).

## Specs

Core, so specs come first and are red until it is built:

- `up` builds the image of an agent with `build:` and the agent runs it (a marker file from the Dockerfile is in the agent).
- A second `up` with nothing changed does not rebuild (same tag, no build output).
- A changed file in the context, a changed arg and a changed Dockerfile each make running instances stale.
- `.dockerignore` is honoured: a change in an ignored file changes nothing.
- A Dockerfile with `RUN --mount=type=cache` and a heredoc builds on every platform.
- On Docker, a `FROM` of an image that is not on the engine (other than the harness image) is pulled and the build passes; a
  `# syntax=` line is refused with egzo's message.
- A failing `RUN` shows its own output during `up`, not only its exit code.
- The harness image reaches the build as `EGZO_HARNESS_IMAGE`, and an agent built from it reports its state to the control.
- A spawn after the image was removed says to run `up` again.
- `build:` with `image:` on one agent is a config error naming both; a missing context or Dockerfile names the path.
- The same specs pass on every platform of the matrix ([ci-test-matrix.md](ci-test-matrix.md)); the Podman cells are the ones to
  watch.
