# Roadmap: a CI test matrix for the specs

The specs (`specs/`) are the specification and the status report, but they have only ever run on one developer
machine: `.github/workflows/ci.yml` runs the Go tests, the linters and the hub's tests, never the specs. Until they run on every platform egzo claims to support, "works on Podman" and "gVisor works" are not claims
the docs can make. Background: `known-issues/reference-platform-ci-matrix.md`; how a run picks its platform:
`specs/README.md`.

## Goal

The specs run on each **reference platform**, one machine (or runner) per platform, each with exactly the engine its
scenario names, and the result per platform is public, so the docs' support table is backed by a run instead of a belief.
The repository is public, so GitHub-hosted runners are free and the limits (20 concurrent jobs on the free plan, 256 jobs per
matrix, 6 hours per job) are far from what a matrix of five cells needs.

## Decided

- **GitHub Actions, hosted runners.** A matrix cell is a job, a job is a fresh VM, so a cell never shares a kernel, a daemon or
  a network pool with another: Docker and Podman are never on the same machine. `ubuntu-26.04` carries Docker and Podman
  already (the runner image README lists Docker 29.4.2 and Podman 5.7.0); gVisor and the Podman API sockets are set up by
  `scripts/ci-engine.sh`.
- **When it runs** (`.github/workflows/specs.yml`, one reusable workflow):

  | Event | Platforms | Blocks? |
  |---|---|---|
  | `pull_request` | `docker` (the whole matrix while the workflow is tried out, `PR_FULL` in `specs.yml`) | yes, once it is a required check |
  | `merge_group` (the merge queue) | all | yes: the gate in front of `main` |
  | nightly, and by hand | all | no; a red night is a finding |
  | a release tag (`release.yml` calls it first) | all | yes: nothing is built or pushed if a required cell fails |

  A pull request gets the Docker cell for a fast answer; the whole matrix runs once per merge, in the queue, on the result of
  merging, so `main` never gets a change a platform rejects. The nightly run catches what no push touched: a new runner image
  (an engine version), a new engine release, an online service the specs reach going away.
- **Cells that are not proven are `experimental`**: they run and show, and cannot fail the run. `docker` is required from the
  start. `docker-gvisor`, `podman-rootless` and `docker-arm64` become required, one by one, after a clean history, by
  removing the flag in `specs.yml` and adding the check to the branch rules. Docker and rootless Podman are supported platforms.
  Rootful Podman (`podman`) is not supported for now: it has a diagnosed intermittent failure
  (`known-issues/rootful-podman-intermittent-egress.md`, netavark leaves a stale per-bridge sysctl file that switches forwarding off
  on a reused bridge name, containers/netavark#1497). Its cell stays `experimental` and its specs stay red, as they say what is true.
  Whether it is supported in 1.0.0, probably with a minimum Podman and netavark version, is decided then
  ([initial-release.md](initial-release.md)).
- **Serial.** Each cell runs the specs one at a time. Four `pytest-xdist` workers (`--dist loadfile`) were tried on the first
  runs: locally with Docker and gVisor they took 3 minutes instead of about 12, but 54 specs failed on `all predefined address
  pools have been fully subnetted` (every project and instance is a network and Docker's default pool holds about 30), and with
  the pool widened the timing-sensitive specs (a terminal's colours, the pty attach, the browser, the stand-in agents' timings)
  failed on three platforms. Serial passes on every Docker cell. Stable runs are preferred to fast ones, so xdist and the pool
  setting are gone; [specs-overhaul.md](specs-overhaul.md) stands ("not for now").
- **Podman cells.** Things the first runs found that are about the host, not egzo, and that `scripts/ci-engine.sh` sets up:
  the rootful socket is served by `podman system service` in a `/run/podman` the runner user can enter (systemd leaves it
  root-only); the test registry on `localhost:5000` is declared insecure (Podman does not trust localhost for plain HTTP as Docker
  does); and the rootful cell runs without Ubuntu's AppArmor container profile
  (`known-issues/rootful-podman-apparmor-stacking.md`). gVisor comes from its release archive (`gvisor.tar.bz2`), as the separate
  `runsc` files are gone. Podman 5.7 refuses the `uid=` option of a tmpfs mount, so egzo's sidecar tmpfs use `mode=1777`.
- **Artifacts.** Each cell uploads the log of its run (the summary per area is its last lines) and a JUnit file.

## To do before the matrix is a gate

- The merge queue has to be switched on for `main` in the repository settings ("Require merge queue" in the branch rule or
  ruleset; it needs `merge_group` in the workflow, which `specs.yml` has, and the required check named exactly as the job:
  `specs (docker)`). It is available to public repositories of an organization; check it in the settings.
- Run the cells that were never run on a runner (gVisor, both Podman platforms, arm64) and fix what the first run shows.
  `scripts/ci-engine.sh` for those is written from the documentation of each engine, not run.
- **A red suite is the correct state when specs are added for a feature that is not built** (`CLAUDE.md`). A required check that
  means "all green" would block every pull request that adds such a spec. Today `main` is green, so the check works, but this has to
  be decided before the next spec for an unbuilt feature lands: a baseline of the specs that are allowed to fail, a separate
  required check for "no spec that passed on `main` fails", or merging those specs on a branch that is not gated.

## The matrix

| axis | values |
|---|---|
| platform (`pytest --engine`) | `docker`, `docker-gvisor`, `podman`, `podman-rootless` |
| operating system | the distributions we choose to support (to be decided: for example Ubuntu LTS, Debian stable, Fedora) |
| kernel, cgroup version, firewall backend | whatever each OS ships; record it in the run output, since it changes how rootful Podman and rootless networking behave |

`podman-gvisor` is not in the matrix: egzo never chooses a runtime, so it would be Podman with `runsc` as the default in `containers.conf`, which nobody has set up yet.

A cell provides what its scenario names and nothing else: a `docker` cell has no `runsc`; a `docker-gvisor` cell has
`runsc` as the engine's default runtime; `podman-rootless` has a user socket and no rootful one.

## What a cell must show

- The suite runs to the end with the platform's expected result. Specs that need a feature the platform lacks (none
  today) are strict xfail there, so a cell that **unexpectedly passes or fails is a finding**,
  not noise.
- The summary per area (one area per `test_<area>.py`) is kept as an artifact, so a regression on one platform shows as
  a change in a column.
- gVisor is checked on a pair of platforms: an agent really runs under gVisor where it is the engine's default runtime,
  and does not where it is not (`test_agents_run_under_the_engines_default_runtime…`).
- Known host-level problems stay visible: the rootful Podman intermittent egress failure
  (`known-issues/rootful-podman-intermittent-egress.md`) should show up as a flaky cell and not be hidden by retries.

## Work to do

- [x] The runner type: GitHub-hosted, a fresh VM per cell. The supported operating systems are still the runner's (Ubuntu 26.04);
      a second distribution (Debian, Fedora) would be a container or another runner and is not decided.
- [x] Provision each scenario reproducibly: `scripts/ci-engine.sh <platform>`, versioned with the repo.
- [x] A workflow that builds egzo and runs the specs per cell (`specs.yml`; each cell builds the images it needs on its own engine,
      so nothing is shared between cells).
- [x] What runs when: see "Decided".
- [ ] Disk: a cell builds the sidecar and both harness images (Chromium) on a 14 GB disk. `specs.yml` removes the preinstalled
      toolchains and prints `df`; check the first run.
- [ ] Publish the results: a status table in the docs, replacing "works on this machine" with the platforms that
      actually pass. Update `docs/install.md` (engine table) and `docs/troubleshooting.md` (known problems) from it.
- [ ] Triage the first full run: fix, or document in `known-issues/`, whatever fails on the platforms never run before
      (rootful and rootless Podman, `docker-gvisor`).
- [ ] Keep unit tests (`go test -race ./...`) and `make lint` (staticcheck, govulncheck) as separate, cheaper jobs.

## Related

[Initial release](initial-release.md) depends on this: the platform claims in the docs are only as good as the matrix.
