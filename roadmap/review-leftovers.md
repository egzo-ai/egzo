# Roadmap: leftovers of the first code review

What was still open when `TODO.md` (the review item list, R-nn and T-nn) was retired. Everything else in it was
fixed, obsolete (it described `depends_on`, `up AGENT` and `project.json`, before agents became templates), or
covered by specs and unit tests.

- **Audit sampling** (R-11, R-48): fields are capped and bytes and duration are logged, but repeated denials are not
  aggregated or sampled. `specs/*.yaml` retention (R-21) was not re-verified.
- **Podman uid mapping** (R-42): rootless Docker and Podman keep the image's user; the mapping is an open design
  question to record in DESIGN.md.
- **Per-request cost** (R-47): `Observe` still runs once per command, `agentTokens` has no bound, and
  `mcp.NewServer` is created per request (not checked).
- **Stack unit tests** (T-06): a fake of `client.APIClient` was not built; `Up`, `Apply`, `Down` and `RunPrep` are
  covered by specs only.
- **CLI unit tests** (T-07): the `down --workspaces` flow and `send --wait` exit codes are covered by specs only.
- **Moby client migration** (T-12): blocked upstream; `govulncheck` flags GO-2026-4887 (server side, no fix).
- **`errcheck` sweep**: unchecked `Close` and `Write` returns (for example `session/client.go`) were not worked
  through; `staticcheck` is clean and `make lint` runs it with `govulncheck`.
