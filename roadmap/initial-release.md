# Roadmap: what is required for 1.0.0

This page tracks what has to be true before egzo 1.0.0, and what to re-check in the documentation then. egzo is already
published as early 0.x releases (tags `0.0.1-rc1` to `0.4.1`; GitHub releases from `0.1.1`), so "initial release" now means
the first one that can be recommended.

The specs must run in CI on every reference platform before 1.0.0 ([ci-test-matrix.md](ci-test-matrix.md)). The solution
(runners, hosted or self-hosted, which CI) is not chosen; the requirement is that the matrix runs somewhere automated.

## Done

- `release.yml` ran. A tag without a `v` (`0.4.1`) builds the `linux/amd64` and `linux/arm64` binaries with `SHA256SUMS`
  (attached to the GitHub release), then the multi-arch sidecar image and the two harness images, pushed to `ghcr.io/egzo-ai/`
  (`egzo`, `egzo-harness-claude-code`, `egzo-harness-opencode`) with provenance. A tag with a `-` is a pre-release and is not
  tagged `latest`. The three packages are public (they pull without a login).
- The harness packages are pinned with build arguments (`CLAUDE_CODE_VERSION`, `OPENCODE_VERSION`, `PLAYWRIGHT_MCP_VERSION`).
- `docs/install.md` tells how to download and check a release.

## Publish

- [ ] **Check the releases.** `0.4.0` has a tag but no GitHub release and no images, and `0.1.0` and `0.0.1-rc1` have images
      but no release: find out why, and make sure a tag cannot half-publish.
- [ ] **TODO: publish the hub image.** `hub/compose.yaml` pulls `ghcr.io/egzo-ai/egzo-hub`, which `release.yml` does not build and ghcr does not
      have, so the hub quick start only works after `make hub-image` and `EGZO_HUB_VERSION=dev`. Add the image to the release (and make the
      package public), then drop those two lines from the quick start in `hub/docs/README.md`.
- [ ] `ci.yml` should also build the images without pushing, so a broken Dockerfile shows before a tag does.
- [ ] **arm64 has to be run**, not only built: no spec runs on arm64, and the browser has not been tried there. Until then do not
      claim arm64 support beyond "builds".
- [ ] Optional: keyless cosign signing of the images and the binaries.
- [ ] Native arm runners (`ubuntu-26.04-arm`) instead of QEMU, if the arm64 builds are too slow.
- [ ] An install wrapper (after the plain download works, which it now does): a script that detects the platform, downloads the
      right binary, verifies its checksum and installs it, for a `curl ... | sh` style install and perhaps a package
      (Homebrew tap, `.deb`). Then update `docs/install.md` to lead with it and keep the manual download as the fallback.
- [ ] Check the name, domain, org and registry (DESIGN.md, "Naming": availability was never really checked).
- [ ] **A CI matrix running the specs** on every reference platform ([`ci-test-matrix.md`](ci-test-matrix.md),
      `known-issues/reference-platform-ci-matrix.md`). `ci.yml` runs only the Go tests, linters and hub tests today. The CI solution is
      undecided; until the matrix runs, "supported on Podman" or "gVisor works" are not claims to make.
- [ ] Decide whether Podman is supported in the first release, given
      `known-issues/rootful-podman-intermittent-egress.md` and `known-issues/podman-gvisor-unsupported.md`.
- [ ] Retire [`review-leftovers.md`](review-leftovers.md): file its concrete items as GitHub issues (the `errcheck` sweep, a fake
      `client.APIClient` for `Up`/`Apply`/`Down`/`RunPrep` unit tests, audit-log sampling of repeated denials, and a link to
      `known-issues/moby-daemon-advisories.md` for the `govulncheck` finding), fold the Podman uid mapping and per-request cost
      notes into `DESIGN.md` or drop them, then delete the file and this link. Its R-nn and T-nn ids refer to the deleted
      `TODO.md` and mean nothing to outsiders.

## Documentation review against the published artifacts

Done in the 2026-10-15 housekeeping pass: every `egzo` command in `docs/`, `README.md`, `DESIGN.md` and `specs/README.md` exists
in the CLI, the install page and the status paragraphs no longer say there is no release, and the claims about workspaces
(engine volumes, no host path), the sidecars (non-root uid, read-only root) and the message limits match the code.

What a pass on a **clean machine, from the published binary and images**, still has to check:

- [ ] `docs/install.md`: the download, checksum and `PATH` steps as written, and which image tags exist (a version, `latest`).
      Update it again when the install wrapper lands.
- [ ] `docs/intro.md`: the first run (`egzo project init`, `project up`, `agent spawn --attach`) end to end.
- [ ] `docs/images.md`: the `FROM ghcr.io/egzo-ai/egzo-harness-claude-code:latest` example and the derived-image rules
      (entrypoint, uid, no `CMD`) against a real build and `agent spawn`.
- [ ] `docs/harnesses.md`: the "Images" section (default name, `EGZO_HARNESS_PREFIX`, the error for an image that cannot be pulled).
- [ ] `docs/troubleshooting.md`: the pull-failure row, and "Known problems", against what is still true.
- [ ] Every YAML example run through `egzo project check --resolved`. The examples were written from the design and the code,
      and were not all executed.
- [ ] Unverified claims: that `agent attach` works for a `custom` harness image; the exact placeholder credentials each harness
      receives.
- [ ] Dead links: `docs/` links to `../README.md`, `roadmap/` and `known-issues/` by path.

## Later

The other roadmap items are separate: [hub](hub.md) and [messaging adapters](messaging-adapters.md), [ssh access for
agents](ssh-access.md), [review leftovers](review-leftovers.md), [CI test matrix](ci-test-matrix.md), [macOS and Windows (WSL2) hosts](windows-and-macos.md).
