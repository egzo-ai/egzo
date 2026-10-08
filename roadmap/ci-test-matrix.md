# Roadmap: a CI test matrix for the specs

The specs (`specs/`) are the specification and the status report, but they have only ever run on one developer
machine. Until they run on every platform egzo claims to support, "works on Podman" and "gVisor works" are not claims
the docs can make. Background: `known-issues/reference-platform-ci-matrix.md`; how a run picks its platform:
`specs/README.md`.

## Goal

Every push runs the specs on each **reference platform**, one machine (or runner) per platform, each
with exactly the engine its scenario names, and the result per platform is public, so the docs' support table is
backed by a run instead of a belief.

## The matrix

| axis | values |
|---|---|
| platform (`pytest --engine`) | `docker`, `docker-gvisor`, `podman`, `podman-rootless` |
| operating system | the distributions we choose to support (to be decided: for example Ubuntu LTS, Debian stable, Fedora) |
| kernel, cgroup version, firewall backend | whatever each OS ships; record it in the run output, since it changes how rootful Podman and rootless networking behave |

`podman-gvisor` is not in the matrix: Podman's API cannot select a runtime (`known-issues/podman-gvisor-unsupported.md`).

A cell provides what its scenario names and nothing else: a `docker` cell has no `runsc`; a `docker-gvisor` cell has
it registered; `podman-rootless` has a user socket and no rootful one.

## What a cell must show

- The suite runs to the end with the platform's expected result. Specs that need a feature the platform lacks (today:
  `runtime: runsc` without gVisor) are strict xfail there, so a cell that **unexpectedly passes or fails is a finding**,
  not noise.
- The summary per area (one area per `test_<area>.py`) is kept as an artifact, so a regression on one platform shows as
  a change in a column.
- gVisor is checked on a pair of platforms: a project that asks for `runtime: runsc` starts and really runs under gVisor
  where it is registered, and cannot start where it is not.
- Known host-level problems stay visible: the rootful Podman intermittent egress failure
  (`known-issues/rootful-podman-intermittent-egress.md`) should show up as a flaky cell and not be hidden by retries.

## Work to do

- [ ] Choose the supported operating systems and the runner type (hosted VMs with Docker and Podman, or self-hosted
      machines; gVisor and rootful Podman need root and a recent kernel, which rules out some hosted runners).
- [ ] Provision each scenario reproducibly (a script or image per cell, versioned with the repo), so a cell is not a
      hand-built pet machine.
- [ ] A workflow that builds the CLI and images once, then fans out `make specs ENGINE=<scenario>` per cell.
- [ ] Cost and time: the suite takes about 7 minutes on one platform. Decide what runs on every push (a Docker cell) and
      what runs on a schedule or before a release (the full matrix).
- [ ] Publish the results: a status table in the docs, replacing "works on this machine" with the platforms that
      actually pass. Update `docs/install.md` (engine table) and `docs/troubleshooting.md` (known problems) from it.
- [ ] Triage the first full run: fix, or document in `known-issues/`, whatever fails on the platforms never run before
      (rootful and rootless Podman, `docker-gvisor`).
- [ ] Keep unit tests (`go test -race ./...`) and `make lint` (staticcheck, govulncheck) as separate, cheaper jobs.

## Related

[Initial release](initial-release.md) depends on this: the platform claims in the docs are only as good as the matrix.
