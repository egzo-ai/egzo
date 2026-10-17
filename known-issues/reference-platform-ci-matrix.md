# The specs have only ever run on one developer machine

Status: **open** (written 2026-10-04). Needs CI infrastructure.

## What the engine scenarios are

`--engine` picks a **reference platform**, which says what the host the specs run on is like, not what
egzo is asked to do:

| scenario | the host is |
|---|---|
| `docker` | a typical rootful Docker host, no gVisor |
| `docker-gvisor` | the same, with `runsc` as the engine's default runtime |
| `podman` | a rootful Podman host, no gVisor |
| `podman-rootless` | a rootless Podman host, no gVisor |

egzo never chooses a container runtime, so a `-gvisor` scenario is a host whose engine has `runsc` as its default
runtime: everything it starts runs under gVisor. There is no `podman-gvisor` scenario yet (it would be Podman with
`runsc` as the default in `containers.conf`).

## What is missing

- No spec has run on any host except this developer machine (Docker, gVisor available, rootless and
  rootful Podman installed). `.github/workflows/ci.yml` runs the Go unit tests, the linters and the hub's tests, never the specs. The Podman and gVisor scenarios were never run in full.
- gVisor can only be checked properly on a pair of reference platforms, one with gVisor as the default runtime and one
  without: "an agent runs under gVisor on the first and not on the second" has to be seen on real hosts.
- The reference platforms are not defined beyond the engine: operating system, distribution, kernel,
  cgroup version and firewall backend all change how rootful Podman and rootless networking behave
  (see `rootful-podman-intermittent-egress.md`).

## What to set up

A CI matrix, one machine per reference platform, each running `pytest --engine <scenario>`:
engine scenarios x the operating systems and distributions we support (to be chosen). Each cell
provides what its scenario names, and nothing else. A spec that needs a feature a scenario lacks is a strict xfail there, so a cell that unexpectedly passes or fails is a finding.

## Until then

Do not read a green local run as "supported on Podman" or "gVisor works". It means "works on this
machine, for the scenario that was selected".

The plan for it is in `roadmap/ci-test-matrix.md`.
