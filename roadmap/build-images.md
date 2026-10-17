# Roadmap: `build:` on an agent (Compose style)

**Status.** Designed, not built. Specs first (see "Specs"). Not needed for 1.0.0.

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
- **Build output.** Podman's build output is not always valid UTF-8 (the specs already met it); read it as bytes.
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
- The harness image reaches the build as `EGZO_HARNESS_IMAGE`, and an agent built from it reports its state to the control.
- A spawn after the image was removed says to run `up` again.
- `build:` with `image:` on one agent is a config error naming both; a missing context or Dockerfile names the path.
- The same specs pass on every platform of the matrix ([ci-test-matrix.md](ci-test-matrix.md)); the Podman cells are the ones to
  watch.
