# Overriding base images

The stock harness images ([harnesses](harnesses.md)) are small: Ubuntu 26.04 with git, Python 3 (with `venv`), ripgrep, jq and curl, Node (its LTS, the official build), a browser and the harness. They all share one base image, `egzo-harness-base`, which carries everything but the harness itself. If your agents need a compiler, a
language runtime, `psql` or a linter, build an image of your own on top of the stock one and name it in the template.

## Extend a stock image

```dockerfile
# Dockerfile.agent
FROM ghcr.io/egzo-ai/egzo-harness-claude-code:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential postgresql-client \
 && rm -rf /var/lib/apt/lists/*
```

```console
$ docker build -f Dockerfile.agent -t registry.example/shop-agent:1 .
```

```yaml
agents:
  coder:
    harness: claude-code
    image: registry.example/shop-agent:1
    workspaces: [repo]
```

`image:` replaces the default image name; everything else about the harness stays: the entrypoint (`egzo agent run`),
hooks, MCP and the home volume. Use the same tag as your `egzo` version if you want to be sure the in-container
binary matches the CLI (`egzo version`; the stock images are tagged with it).

The stock images already carry a browser ([browsing](browser.md)): Chromium, the Playwright MCP server (`playwright-mcp` on the
`PATH`) and `certutil`. The harness registers the browser only when `playwright-mcp` is on the `PATH`, and `egzo agent run`
adds the project CA to Chromium's store when `certutil` is, so an image built `FROM` a stock one has both. An image that
removes them has no browser server rather than one that cannot start. The browser adds about 710 MB to the image
uncompressed (about 220 MB to what is pulled) on top of the bare harness. The images are built for linux/amd64 and
linux/arm64 from the same Dockerfile, but the browser has only been tried on amd64.

Rules for a derived image:

- **Keep the entrypoint.** Do not set your own `ENTRYPOINT`. It starts the session holder, which is what makes
  `agent attach`, messages and status work. Do not set `CMD` either.
- **Install as root, run as nobody in particular.** Egzo runs the agent as your uid and gid, whatever `USER` says,
  so tools must be world-readable and executable, and anything the agent writes must go to the workspace, `/tmp` or
  its home. `/home/agent` and `/workspace` are writable by everyone.
- **Do not bake secrets in.** Credentials come from the proxy. An image is not a place for a token, and nothing
  in it can be injected.
- **Network is the proxy.** `apt`, `pip` and `npm` at *run* time work only for hosts in the agent's profile ([sandboxing](sandboxing.md))
  (`allow: ["*.pypi.org", files.pythonhosted.org]`). Installing at *build* time, as above, needs no egress rule at
  all, and is the better place for tools.
- **Prefer the image to a runtime install.** Anything an agent installs into its home volume survives, but
  anything it installs system-wide does not, and an image gives every instance the same tools.

After changing the image tag, the template changes, so running instances become stale: `egzo project up`, `egzo agent rm`, spawn again.

## Let egzo build it

Instead of `image:`, give the agent a `build:`, with Compose's keys, and `egzo project up` builds the image:

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

As in Compose, `build: ./agent` is short for `build: { context: ./agent }`.

```dockerfile
# agent/Dockerfile
ARG EGZO_HARNESS_IMAGE
FROM ${EGZO_HARNESS_IMAGE}
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential postgresql-client \
 && rm -rf /var/lib/apt/lists/*
```

- **`EGZO_HARNESS_IMAGE`** is a build argument egzo sets to the stock image of the agent's harness, at the CLI's version, so
  the `egzo` binary in the image always matches the CLI. The `custom` harness has none, and takes a `build:` instead of an
  `image:`. `build:` and `image:` exclude each other.
- **Built by content.** The image is tagged `localhost/egzo-build/<project>-<agent>:<hash>`, the hash covering the context (less
  what `.dockerignore` excludes), the Dockerfile, the arguments, the target and the harness image. `up` builds only when that
  tag is not on the engine, so an unchanged context is not built again. A changed one makes the agent's instances stale, as
  a new `image:` does. `up` removes the images earlier builds of the project left, and `project down` removes the project's.
- **Through the engine.** egzo sends the context to the engine's build endpoint: Docker builds with BuildKit, Podman with
  buildah. No `docker` or `podman` command and no buildx plugin is needed. The build's steps and output are printed by `up`.
- **Base images, on Docker.** Docker cannot fetch an image during a build that comes through its API, so egzo pulls the harness
  image first, and any other image the build reports missing, with your `docker login` credentials, then builds again.
- **`# syntax=` is not supported on Docker,** for the same reason. The built-in parser handles heredocs and `RUN --mount`.
- **The Dockerfile is trusted.** A build runs on the engine, outside the egress proxy: its `RUN` steps reach whatever the engine
  can, as when you build by hand.
- **Only `up` builds.** A spawn, from the CLI or [the hub](hub.md), uses the image `up` built. If it was removed since (`docker
  image prune`), the spawn says to run `egzo project up` again.

The rules above for a derived image apply to a built one too.

## Pin it

Use a version tag, not `latest`, for anything you share. A moving tag means two instances of one template can
differ.

## Building the stock images yourself

(See also [installing](install.md).)

```console
$ make images                   # builds egzo and egzo-harness-{claude-code,opencode}
$ EGZO_HARNESS_PREFIX=registry.example/team egzo agent spawn coder
```

`EGZO_HARNESS_PREFIX` moves where the default names are looked up, so a team can mirror the stock images.

## The `custom` harness

`harness: custom` takes any image and runs it as given:

```yaml
agents:
  linter:
    harness: custom
    image: docker.io/library/alpine:3
```

The sandbox still applies in full: the agent has its own network with the proxy as its only way out, no secrets, your uid and the
resource limits, and it runs under whatever runtime the engine defaults to (gVisor, if that is what you set).

What you do not get is the harness integration. There are no hooks, no MCP tools and no home volume, so egzo judges activity from
how quiet the output is. Use `custom` to run a script, or a tool egzo has no integration for yet, such as `pi`.

## ssh in an image of your own

The egzo harness images carry what `ssh` needs ([ssh to servers](sandboxing.md#ssh-to-servers)). In an image of your own: install `openssh-client`;
if the entrypoint is not `egzo agent run`, run `egzo agent ssh-setup` when the container starts (it writes the instance's identity and the servers of
its profile into `~/.ssh`). Nothing else: egzo gives the agent's user (the uid of whoever started the project) a passwd entry whose home is `$HOME`,
which ssh needs.
