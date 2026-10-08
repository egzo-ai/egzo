# Security policy

egzo sandboxes coding agents, so security reports are taken seriously.

## Reporting a vulnerability

Please do not open a public issue. Report privately through GitHub's
["Report a vulnerability"](https://github.com/egzo-ai/egzo/security/advisories/new) form on this repository.

Include what you found, how to reproduce it, and the engine and platform you ran on (Docker, gVisor, Podman,
rootless Podman). Sandbox escapes, credential leaks past the proxy, and egress policy bypasses are in scope.

See [`docs/security.md`](docs/security.md) for the threat model.
