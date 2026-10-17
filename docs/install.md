# Installing and choosing an engine

## Supported platforms

**Linux** with Docker or rootless Podman is the supported host today. Rootful Podman is not supported for now (see the table below). The specs describe reference platforms, and so far they
have run on one developer machine only (`known-issues/reference-platform-ci-matrix.md`), so read "supported" as
"built and tried on Linux with Docker", not as a tested matrix.

**Planned:** Windows through WSL2 (the Linux binary inside a WSL2 distro, with Docker Desktop) and macOS (with Docker
Desktop, Colima or OrbStack). Neither is supported yet; see
[`roadmap/windows-and-macos.md`](../roadmap/windows-and-macos.md). There is no plan for a native Windows binary.

## Get egzo

Egzo is a single static binary. Each [release](https://github.com/egzo-ai/egzo/releases) has `egzo-linux-amd64`,
`egzo-linux-arm64` and a `SHA256SUMS` file. Download the one for your machine, check it, make it executable and put it on
your `PATH`:

```console
$ curl -fsSLO https://github.com/egzo-ai/egzo/releases/latest/download/egzo-linux-amd64
$ curl -fsSLO https://github.com/egzo-ai/egzo/releases/latest/download/SHA256SUMS
$ sha256sum --ignore-missing -c SHA256SUMS
$ install -m 0755 egzo-linux-amd64 ~/.local/bin/egzo      # or /usr/local/bin with sudo, any directory on your PATH
$ egzo version
```

That is all the CLI needs. It talks to your container engine, and pulls the images it uses the first time you
`egzo project up` or `egzo agent spawn`: the sidecar image `ghcr.io/egzo-ai/egzo:<version>` and the harness image
`ghcr.io/egzo-ai/egzo-harness-<name>:<version>` for each harness you use. The CLI always uses the images tagged with its
own version, so the CLI and the sidecars cannot drift. To pull from another registry (a mirror), set
`EGZO_HARNESS_PREFIX`. If an image cannot be pulled, `egzo agent spawn` says which image and how to override it.

> There is no install script yet, and the releases are early (0.x): see [`roadmap/initial-release.md`](../roadmap/initial-release.md).

## Pick an engine

Egzo uses the engine `DOCKER_HOST` points at, like the docker CLI (default `/var/run/docker.sock`). There is no
engine setting in `egzo.yaml`.

| engine | status |
|---|---|
| Docker (rootful) | the engine the specs run against; use this first |
| Docker with gVisor | supported; make `runsc` the engine's default runtime |
| Rootless Podman | supported; works through the user socket, see below; no gVisor |
| Rootful Podman | not supported for now: a netavark bug can leave a project without outbound connectivity (`known-issues/rootful-podman-intermittent-egress.md`); to be decided for 1.0.0, probably with a minimum Podman and netavark version |

Rootless Podman:

```console
$ systemctl --user enable --now podman.socket
$ export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
```

gVisor on Docker: install `runsc`, register it under `"runtimes"` in `/etc/docker/daemon.json` and set
`"default-runtime": "runsc"`, then restart Docker. Register it with `--reproduce-nat` (`runsc install -- --reproduce-nat`, or `runtimeArgs`
in the `daemon.json` entry): without it gVisor does not reproduce Docker's embedded DNS, and containers cannot find each other by name. egzo never chooses a container runtime: everything the engine
starts, agents and sidecars, runs under its default one, which is the host administrator's choice.

Agents run as your uid and gid, and egzo never mounts the engine socket into an agent. Anything that can reach the
engine socket is root-equivalent on a rootful engine, so a rootless engine is the safer host.

## Check the setup

(When something fails, [troubleshooting](troubleshooting.md) is the next page; [the security model](security.md) explains
why a rootless engine and gVisor matter.)

```console
$ egzo system check
```

It tells you whether the engine answers, whether it is rootless, which OCI runtime the engine's containers run under (`runsc` is gVisor), whether the engine
creates internal networks (the sandbox depends on it), whether the engine's default log driver can be read back and
followed, and whether the egzo image can run git. Each line is ok, warn or fail, and the exit code is non-zero if any check fails.

### Logging

egzo does not choose a log driver or set log options: every container gets **the engine's default logging**, which is
your choice (journald is common). Two things follow.

- **Bound the logs yourself.** Docker's default `json-file` driver keeps logs forever, so set `log-opts` with `max-size`
  and `max-file` in `/etc/docker/daemon.json` (or use the `local` driver, or journald with its own retention), and for
  Podman `log_size_max` in `containers.conf`. A new default applies to containers created afterwards: run `egzo project up`
  again after recreating them. `egzo system check` warns when the default is a `json-file` with no limit.
- **The default must be readable.** egzo reads container logs back: for the git jobs, for `egzo logs`, and for the agents'
  status that the hub follows. `json-file`, `local` and `journald` work on Docker, `k8s-file` and `journald` on Podman. The
  `none` driver, and Podman's `passthrough`, cannot be read, so `egzo system check` fails on them. Docker's remote drivers
  (`syslog`, `gelf`, `fluentd`, `awslogs`, ...) are read through Docker's local cache of them, which works unless it is
  turned off. See `known-issues/container-logging-follows-the-engine-default.md`.

## Building from source

For development, or to run something that is not released. You need Go 1.27 or later, Docker (to build images) and `make`:

```console
$ git clone https://github.com/egzo-ai/egzo && cd egzo
$ make build            # bin/egzo
$ make images           # builds the sidecar image and the harness images locally
$ bin/egzo version
```

`make images` builds `ghcr.io/egzo-ai/egzo:<version>` and `ghcr.io/egzo-ai/egzo-harness-{claude-code,opencode}:<version>`
in your local engine, so a built `egzo` finds them without pulling. Put `bin/egzo` on your `PATH`, or run it by path.
