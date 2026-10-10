# CI lints with a pre-release staticcheck

Status: **accepted, temporary** (written 2026-10-10).

CI installs `staticcheck` from a go-tools commit, [f1838cc](https://github.com/dominikh/go-tools/commit/f1838cc308e5cfbb38d91cfc973355611845997e),
instead of a released version (`.github/workflows/ci.yml`).

Go 1.27.2 writes compiled package data (export data) in a newer format, version 5. The latest `staticcheck` release,
2026.2.1, reads at most version 4, so it fails on every package with `export data version 5 is greater than maximum
supported version 4` ([go-tools#1832](https://github.com/dominikh/go-tools/issues/1832)). We need Go 1.27.2 for nine
standard library advisories that `govulncheck` reports on 1.27.1, so we cannot stay on the older Go. The fix is on
go-tools master (it updates `golang.org/x/tools`) but was not in a tagged release when this was written.

A commit is pinned, not `@master`, so a lint result does not change under us. `staticcheck ./...` is clean on this
commit.

## Revisit

When go-tools tags a release newer than 2026.2.1, go back to `staticcheck@latest` and delete this file.
