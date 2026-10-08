# Roadmap: initial release

What has to be true before egzo is published, and what to re-check in the documentation when it is. The docs in
`docs/` were written against a source build, before any image or binary was published, so parts of them are
promises that have not been checked against a real release.

## Publish

- [ ] A `release.yml` GitHub Actions workflow, written after the repository is public and triggered by a version tag
      (first tested with a `v0.0.1-rc1` tag). One tag builds everything, so the image tag and `egzo version` agree:
      the binaries and `SHA256SUMS` for the GitHub release, the multi-arch sidecar image, then the harness images
      (they `COPY --from` the sidecar image). It pushes to ghcr.io with `GITHUB_TOKEN` (`packages: write`), with
      provenance and, optionally, keyless cosign signing. `ci.yml` should also build the images without pushing.
      Decided: linux/amd64 and linux/arm64 from the start, so the arm64 harness images have to be run and checked
      before claiming support (no spec runs on arm64 yet); pin the harness npm packages (`@anthropic-ai/claude-code`,
      `opencode-ai`) with build args instead of installing whatever is latest.
- [ ] After the first push, set the three ghcr packages (`egzo`, `egzo-harness-claude-code`, `egzo-harness-opencode`)
      to public and link them to the repository; they start private, and nobody else can pull them until then.
- [ ] Prebuilt images on `ghcr.io/egzo-ai/`: `egzo` (the sidecar image) and `egzo-harness-claude-code`,
      `egzo-harness-opencode`, tagged with the CLI version (`make images` builds the same set).
- [ ] A released `egzo` binary per platform (Linux amd64 and arm64 to start), attached to a GitHub release with
      checksums, so users download it and put it on their `PATH` and do not need Go 1.27 to try it.
- [ ] An install wrapper (later, after the plain download works): a script that detects the platform, downloads the
      right binary, verifies its checksum and installs it, for a `curl ... | sh` style install and perhaps a package
      (Homebrew tap, `.deb`). Then update `docs/install.md` to lead with it and keep the manual download as the fallback.
- [ ] Check the name, domain, org and registry (DESIGN.md, "Naming": availability was never really checked).
- [ ] A CI matrix of reference platforms ([`ci-test-matrix.md`](ci-test-matrix.md),
      `known-issues/reference-platform-ci-matrix.md`): until then the specs have
      only run on one machine, and "supported on Podman" or "gVisor works" are not claims to make.
- [ ] Decide whether Podman is supported in the first release, given
      `known-issues/rootful-podman-intermittent-egress.md` and `known-issues/podman-gvisor-unsupported.md`.
- [ ] Once the GitHub project is live, retire [`review-leftovers.md`](review-leftovers.md): file the four concrete items
      as GitHub issues (the `errcheck` sweep, a fake `client.APIClient` for `Up`/`Apply`/`Down`/`RunPrep` unit tests,
      audit-log sampling of repeated denials, and a `known-issues/` entry for the `govulncheck` finding GO-2026-4887),
      fold the Podman uid mapping and per-request cost notes into `DESIGN.md` or drop them, then delete the file and
      this link. Its R-nn and T-nn ids refer to the deleted `TODO.md` and mean nothing to outsiders.

## Documentation review once images are published

The docs need a pass by someone who installs egzo from the published artifacts on a clean machine and follows
them. Known places to fix:

- [ ] `docs/install.md`: the download instructions name a releases page and a binary that do not exist yet. Check the
      real asset names, `chmod`/`PATH` steps and checksum instructions, remove the "not released yet" note, and state
      which image tags exist (a version, `latest`?). Update it again when the install wrapper lands.
- [ ] `docs/intro.md`: the first run (`egzo init`, `up`, `spawn --attach`) should be run end to end from the
      published binary and images, and its `Requirements` and `Status` sections updated.
- [ ] `docs/images.md`: the `FROM ghcr.io/egzo-ai/egzo-harness-claude-code:latest` example must name a tag that
      exists; check the advice to match the tag to `egzo version`, and the derived-image rules (entrypoint, uid, no
      `CMD`) against a real build and `spawn`.
- [ ] `docs/harnesses.md`: the "Images" section (default name, `EGZO_HARNESS_PREFIX`, the error for an image that
      cannot be pulled) should be checked against the registry as published.
- [ ] `docs/troubleshooting.md`: the pull-failure row, and the "Known problems" list, against what is still true.
- [ ] Every command and flag in the docs against `egzo --help` of the released build, and every YAML example run
      through `egzo config`. The examples were written from the design and the code, and were not all executed.
- [ ] Unverified claims to confirm or remove: that `attach` works for a `custom` harness image; that sidecars run
      as a non-root uid (`docs/security.md`); the exact placeholder credentials each harness receives.
- [ ] Dead links: `docs/` links to `../README.md`, `roadmap/` and `known-issues/` by path.
- [ ] The top-level `README.md` status paragraph and "Requirements" section, which says images come from
      `ghcr.io/egzo-ai/egzo-harness-<name>` or from `make images`.

## Later

The other roadmap items are separate: [hub and web UI](hub-and-web-ui.md), [ssh access for
agents](ssh-access.md), [review leftovers](review-leftovers.md), [CI test matrix](ci-test-matrix.md), [macOS and Windows (WSL2) hosts](windows-and-macos.md).
