# The specs have only ever run on one developer machine

Status: **open** (written 2026-10-04). Needs CI infrastructure.

## What the engine scenarios are

`--engine` picks a **reference platform**, which says what the host the specs run on is like, not what
egzo is asked to do:

| scenario | the host is |
|---|---|
| `docker` | a typical rootful Docker host, no gVisor |
| `docker-gvisor` | the same, with `runsc` registered with Docker |
| `podman` | a rootful Podman host, no gVisor |
| `podman-rootless` | a rootless Podman host, no gVisor |
| `podman-gvisor` (new, not supported yet) | a rootful Podman host with gVisor available |

A `-gvisor` scenario does not put any agent under gVisor. It only describes a host that could run one.
Specs that use the gVisor feature (`runtime: runsc` on an agent) are expected to fail on a platform
without it, and to pass on one with it.

## What is missing

- No spec has run on any host except this developer machine (Docker, gVisor available, rootless and
  rootful Podman installed). The Podman and gVisor scenarios were never run in full.
- A spec that needs gVisor can only be checked properly on a pair of reference platforms, one with
  gVisor and one without: "a project that asks for `runtime: runsc` starts and really runs under gVisor
  on the first, and cannot start on the second" has to be seen failing and passing on real hosts.
- The reference platforms are not defined beyond the engine: operating system, distribution, kernel,
  cgroup version and firewall backend all change how rootful Podman and rootless networking behave
  (see `rootful-podman-intermittent-egress.md`).

## What to set up

A CI matrix, one machine per reference platform, each running `pytest --engine <scenario>`:
engine scenarios x the operating systems and distributions we support (to be chosen). Each cell
provides what its scenario names, and nothing else. The gVisor-dependent specs are marked to fail
(xfail) on the scenarios without gVisor, so a cell that unexpectedly passes or fails is a finding.

## Until then

Do not read a green local run as "supported on Podman" or "gVisor works". It means "works on this
machine, for the scenario that was selected".

The plan for it is in `roadmap/ci-test-matrix.md`.
