# The specs run on one operating system, and only Docker is required

Status: **open** (written 2026-10-04, revised 2026-11-02). The plan is in [`roadmap/ci-test-matrix.md`](../roadmap/ci-test-matrix.md).

The specs run on GitHub Actions, one fresh VM per reference platform (`.github/workflows/specs.yml`). `--engine` picks the platform, which says what the host
is like, not what egzo is asked to do:

| scenario | the host is | in CI |
|---|---|---|
| `docker` | a typical rootful Docker host, no gVisor | required |
| `docker-arm64` | the same, on arm64 | experimental |
| `docker-gvisor` | Docker with `runsc` as the engine's default runtime | experimental |
| `podman-rootless` | a rootless Podman host, no gVisor | experimental |
| `podman` | a rootful Podman host, no gVisor | experimental, and not a supported platform |

egzo never chooses a container runtime, so a `-gvisor` scenario is a host whose engine has `runsc` as its default runtime. There is no `podman-gvisor` scenario yet.

## What is missing

- **One operating system.** Every cell is Ubuntu 26.04. Distribution, kernel, cgroup version and firewall backend all change how rootful Podman and rootless
  networking behave (see `rootful-podman-intermittent-egress.md`), so a pass says little about another distribution.
- **Experimental cells cannot fail a run.** A red cell is visible, and nothing stops a merge on it. A pull request runs the Docker cell only, so a change that breaks another
  platform shows up after it lands, on the push to `main` or the nightly run.

## Until then

Read a green cell as "works on Ubuntu 26.04 for that platform", and not as "works on Podman".
