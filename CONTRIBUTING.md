# Contributing

Thanks for your interest in egzo.

- Build and unit tests: `make build test lint`.
- `specs/` is the specification and is run against a real container engine (`make specs ENGINE=docker`, about 7
  minutes). A spec for a feature that is not built yet fails; do not mark it `xfail` or `skip` to get a green run.
  See `specs/README.md` and `CLAUDE.md`.
- Keep changes focused, and add or update a spec or unit test with them.
- By contributing you agree that your contribution is licensed under the [AGPL-3.0](LICENSE), the license of the
  project.
- Security issues go through [`SECURITY.md`](SECURITY.md), not public issues.
