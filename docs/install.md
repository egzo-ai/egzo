# Installing and choosing an engine

## Supported platforms

**Linux** with Docker or Podman is the supported host today. The specs describe reference platforms, and so far they
have run on one developer machine only (`known-issues/reference-platform-ci-matrix.md`), so read "supported" as
"built and tried on Linux with Docker", not as a tested matrix.

**Planned:** Windows through WSL2 (the Linux binary inside a WSL2 distro, with Docker Desktop) and macOS (with Docker
Desktop, Colima or OrbStack). Neither is supported yet; see
[`roadmap/windows-and-macos.md`](../roadmap/windows-and-macos.md). There is no plan for a native Windows binary.

## Get egzo

Egzo is a single static binary. Download the one for your platform from the releases page of
[github.com/egzo-ai/egzo](https://github.com/egzo-ai/egzo), make it executable and put it on your `PATH`:

```console
$ chmod +x egzo
$ sudo mv egzo /usr/local/bin/      # or any directory on your PATH, such as ~/.local/bin
$ egzo version
```

That is all the CLI needs. It talks to your container engine, and pulls the images it uses the first time you
`up` or `spawn`: the sidecar image `ghcr.io/egzo-ai/egzo:<version>` and the harness image
`ghcr.io/egzo-ai/egzo-harness-<name>:<version>` for each harness you use. The CLI always uses the images tagged with its
own version, so the CLI and the sidecars cannot drift. To pull from another registry (a mirror), set
`EGZO_HARNESS_PREFIX`. If an image cannot be pulled, `spawn` says which image and how to override it.

> Egzo has not been released yet, so there is nothing to download today; until it is, build from source (below). The
> release and an install script are tracked in [`roadmap/initial-release.md`](../roadmap/initial-release.md).

## Pick an engine

Egzo uses the engine `DOCKER_HOST` points at, like the docker CLI (default `/var/run/docker.sock`). There is no
engine setting in `egzo.yaml`.

| engine | status |
|---|---|
| Docker (rootful) | the engine the specs run against; use this first |
| Docker with gVisor | supported; register `runsc` and set `runtime: runsc` on agents |
| Podman (through its Docker-compatible socket) | works, not run in full by the specs; no gVisor |
| Rootless Podman | works through the user socket, see below |

Rootless Podman:

```console
$ systemctl --user enable --now podman.socket
$ export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
```

gVisor on Docker: install `runsc`, add it under `"runtimes"` in `/etc/docker/daemon.json`, restart Docker, then
`runtime: runsc` on an agent. Podman's API cannot select or verify a runtime, so a project that asks for one there
fails at `up` instead of silently starting in the default runtime.

Agents run as your uid and gid, and egzo never mounts the engine socket into an agent. Anything that can reach the
engine socket is root-equivalent on a rootful engine, so a rootless engine is the safer host.

## Check the setup

(When something fails, [troubleshooting](troubleshooting.md) is the next page; [the security model](security.md) explains
why a rootless engine and gVisor matter.)

```console
$ egzo doctor
```

It tells you whether the engine answers, whether it is rootless, whether gVisor is registered, whether the engine
creates internal networks (the sandbox depends on it), and problems in the project in this directory (such as `.egzo/`
not being git-ignored). Each line is ok, warn or fail, and the exit code is non-zero if any check fails.

## Building from source

For development, or until a release exists. You need Go 1.27 or later, Docker (to build images) and `make`:

```console
$ git clone https://github.com/egzo-ai/egzo && cd egzo
$ make build            # bin/egzo
$ make images           # builds the sidecar image and the harness images locally
$ bin/egzo version
```

`make images` builds `ghcr.io/egzo-ai/egzo:<version>` and `ghcr.io/egzo-ai/egzo-harness-{claude-code,opencode}:<version>`
in your local engine, so a built `egzo` finds them without pulling. Put `bin/egzo` on your `PATH`, or run it by path.
