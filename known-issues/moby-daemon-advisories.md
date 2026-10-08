# govulncheck reports two Moby advisories with no fix

Status: **accepted** (written 2026-10-08).

`govulncheck` reports two advisories in `github.com/docker/docker` v28.5.2, which has no fixed release:

- [GO-2026-4887](https://pkg.go.dev/vuln/GO-2026-4887): AuthZ plugin bypass when a request body is oversized.
- [GO-2026-4883](https://pkg.go.dev/vuln/GO-2026-4883): off-by-one in plugin privilege validation.

Both are bugs in the Docker **daemon** (AuthZ and plugin handling). egzo only uses the client package to talk to an
engine, never runs a daemon or installs plugins, so the vulnerable code does not run on a user's machine. govulncheck
flags them because the module is required and the client package is imported.

`scripts/govulncheck.sh` (used by `make lint` and CI) fails on every other reachable vulnerability and ignores only these
two IDs. Revisit when `github.com/docker/docker` is replaced by the Moby client modules (see
`roadmap/review-leftovers.md`, "Moby client migration") or a fixed release appears; then drop the IDs from the script.
